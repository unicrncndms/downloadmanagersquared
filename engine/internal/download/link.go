package download

import (
	"fmt"
	"time"
)

const (
	maxResolves   = 3
	maxHTTPErrors = 5 // a server that keeps answering with an error isn't likely to change its mind
)

type LinkStatus string

const (
	LinkPending   LinkStatus = "pending" // has a URL that still needs probing
	LinkProbing   LinkStatus = "probing"
	LinkReady     LinkStatus = "ready"
	LinkResolving LinkStatus = "resolving"  // waiting for the browser to fetch a link via this interface
	LinkNeedsPage LinkStatus = "needs-page" // needs its own link, but no landing page is known yet
	LinkRetrying  LinkStatus = "retrying"   // will be tried again by itself at retryAt
	LinkFailed    LinkStatus = "failed"     // given up; only the user's retry brings it back
	LinkMismatch  LinkStatus = "mismatch"   // serves a different file; excluded from this download
)

// Link is one interface's way to the file. Its methods are the only places
// its status changes.
type Link struct {
	URL      string     `json:"url,omitempty"`
	Cookie   Secret     `json:"cookie,omitempty"`
	Referer  string     `json:"referer,omitempty"`
	Status   LinkStatus `json:"status"`
	Error    string     `json:"error,omitempty"`
	Resolves int        `json:"resolves,omitempty"`
	// HTTPErrors counts error answers in a row; kept across restarts so a dead link still gives up.
	HTTPErrors int `json:"httpErrors,omitempty"`
	// Version is what the link's server said of the file; a mirror's chunks are checked against it.
	Version Version `json:"version,omitzero"`

	retryAt time.Time
	pageHit bool         // got a web page for the source URL; what that means is decided once every interface has answered
	held    *probeResult // a good answer waiting for the main interface's, which decides what the file is
	heldAt  time.Time
}

func (l *Link) probing() { l.Status = LinkProbing }

// requeue: the probe was cut short, so it still has to happen.
func (l *Link) requeue() { l.Status = LinkPending }

// restored: the engine restarted. A probe in flight is gone, and anything
// retrying is due at once.
func (l *Link) restored() {
	if l.Status == LinkProbing || l.Status == LinkRetrying {
		l.Status, l.pageHit, l.held = LinkPending, false, nil
	}
}

// ready: the probe found the file. The HTTP error count stays until data
// actually arrives, so a link whose data requests keep failing still gives up.
func (l *Link) ready() {
	l.Status, l.Error, l.Resolves, l.retryAt = LinkReady, "", 0, time.Time{}
}

// dataArrived: a chunk came through, so the link works.
func (l *Link) dataArrived() { l.HTTPErrors = 0 }

func (l *Link) fail(msg string)     { l.Status, l.Error = LinkFailed, msg }
func (l *Link) mismatch(msg string) { l.Status, l.Error = LinkMismatch, msg }

// unreachable: the interface couldn't get through. Networks come back, so keep trying.
func (l *Link) unreachable(now time.Time, msg string) {
	l.Status, l.Error, l.retryAt = LinkRetrying, "unreachable: "+msg, now.Add(30*time.Second)
}

// httpError: the server answered with an error. Try again in a minute, a few times.
func (l *Link) httpError(now time.Time, msg string) {
	l.HTTPErrors++
	if l.HTTPErrors >= maxHTTPErrors {
		l.Status, l.Error, l.retryAt = LinkFailed, fmt.Sprintf("%s; gave up after %d tries", msg, maxHTTPErrors), time.Time{}
		return
	}
	l.Status, l.Error, l.retryAt = LinkRetrying, msg, now.Add(60*time.Second)
}

// serverBusy: the server said to come back later. Waiting is what it asked
// for, so it costs no try.
func (l *Link) serverBusy(now time.Time, retryAfter time.Duration, msg string) {
	l.Status, l.Error, l.retryAt = LinkRetrying, msg, now.Add(retryAfter)
}

// expired: the link stopped working mid-download; probe it again to find out why.
func (l *Link) expired(msg string) { l.Status, l.Error = LinkPending, msg }

// webPage: the source URL gave this interface a web page. It stays probing until
// every interface has answered and decidePageHitsLocked decides what the page was.
func (l *Link) webPage() { l.Status, l.Error, l.pageHit = LinkProbing, "", true }

// hold keeps a good probe answer back until the main interface has answered.
func (l *Link) hold(now time.Time, r probeResult) { l.held, l.heldAt = &r, now }

// takeHeld hands over the held answer, if any, to be applied now.
func (l *Link) takeHeld() *probeResult {
	r := l.held
	l.held = nil
	return r
}

// answering: this interface's probe is still to come, or still out.
func (l *Link) answering() bool {
	return l.Status == LinkPending || (l.Status == LinkProbing && !l.pageHit)
}

// needResolve: this interface needs its own link from the landing page. It
// reports whether to ask the browser for one.
func (l *Link) needResolve(pageKnown bool, reason string) bool {
	l.pageHit = false
	switch {
	case !pageKnown:
		l.Status = LinkNeedsPage
		l.Error = reason + ". It needs its own link, but the page the download came from is unknown: enter it under Details as the landing page, or paste a fresh link to the file there"
		return false
	case l.Resolves >= maxResolves:
		l.fail(fmt.Sprintf("%s; gave up after %d new links", reason, maxResolves))
		return false
	}
	l.Resolves++
	l.Status, l.Error = LinkResolving, reason
	return true
}

// moveTo starts the link over on another mirror. Only the download's source
// URL gets its cookie and referer: they were the browser's for that site.
func (l *Link) moveTo(url string, st State) {
	*l = Link{URL: url, Status: LinkPending}
	if url == st.URL {
		l.Cookie, l.Referer = st.Cookie, st.Referer
	}
}

// resolved stores the link the browser fetched; it gets probed next.
func (l *Link) resolved(url, cookie, referer string) {
	l.URL, l.Cookie, l.Referer = url, Secret(cookie), referer
	l.Status, l.Error, l.retryAt = LinkPending, "", time.Time{}
}

func (l *Link) resolveFailed(msg string) {
	if l.Status == LinkResolving {
		l.fail("browser: " + msg)
	}
}

// pageKnown: a landing page has been given, so a link waiting for one can resolve.
func (l *Link) pageKnown() {
	if l.Status == LinkNeedsPage {
		l.retry()
	}
}

// wake makes a retrying link due for probing once its pause is over.
func (l *Link) wake(now time.Time) {
	if l.Status == LinkRetrying && !now.Before(l.retryAt) {
		l.Status, l.retryAt = LinkPending, time.Time{}
	}
}

// resume is what starting the download does: anything retrying is tried now.
// After the download got stuck, failed links get another chance too.
// Mismatches stay out either way.
func (l *Link) resume(retryFailed bool) {
	if l.Status == LinkRetrying || retryFailed && l.Status == LinkFailed {
		l.retry()
	}
}

// retry starts the link over from a fresh probe, as the user's Retry does.
func (l *Link) retry() {
	if l.Status != LinkProbing {
		l.Status, l.Error, l.Resolves, l.retryAt, l.HTTPErrors, l.pageHit = LinkPending, "", 0, time.Time{}, 0, false
		l.held = nil
	}
}

// stuck: nothing more will happen with this link unless the user steps in.
func (l *Link) stuck() bool {
	return l.Status == LinkFailed || l.Status == LinkMismatch || l.Status == LinkNeedsPage
}
