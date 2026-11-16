package download

import (
	"testing"
	"time"
)

var start = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func TestLinkGivesUpAfterFiveHTTPErrors(t *testing.T) {
	l := &Link{}
	l.probing()
	now := start
	for i := 1; i <= 4; i++ {
		l.httpError(now, "HTTP 500")
		if l.Status != LinkRetrying {
			t.Fatalf("after %d errors: %s, want retrying", i, l.Status)
		}
		now = now.Add(61 * time.Second)
		l.wake(now)
		if l.Status != LinkPending {
			t.Fatalf("after %d errors and a minute: %s, want pending", i, l.Status)
		}
		l.probing()
	}
	l.httpError(now, "HTTP 500")
	if l.Status != LinkFailed {
		t.Fatalf("after 5 errors: %s, want failed", l.Status)
	}
}

// A link that probes fine but whose data requests keep failing still gives up.
func TestOnlyArrivingDataResetsTheTries(t *testing.T) {
	l := &Link{}
	now := start
	for range 4 {
		l.probing()
		l.httpError(now, "HTTP 500")
		now = now.Add(61 * time.Second)
		l.wake(now)
		l.probing()
		l.ready()
	}
	l.httpError(now, "HTTP 500")
	if l.Status != LinkFailed {
		t.Fatalf("after 5 errors with good probes in between: %s, want failed", l.Status)
	}

	l.retry()
	l.probing()
	l.httpError(now, "HTTP 500")
	l.dataArrived()
	for range 4 {
		l.httpError(now, "HTTP 500")
	}
	if l.Status != LinkRetrying {
		t.Fatalf("data arrived after the first error: %s, want retrying", l.Status)
	}
}

func TestServerPushBackCostsNoTry(t *testing.T) {
	l := &Link{}
	now := start
	for range 10 {
		l.probing()
		l.serverBusy(now, 2*time.Minute, "server busy: HTTP 503")
		if l.Status != LinkRetrying {
			t.Fatalf("%s, want retrying", l.Status)
		}
		l.wake(now.Add(time.Minute))
		if l.Status != LinkRetrying {
			t.Fatal("retried before Retry-After was over")
		}
		now = now.Add(2 * time.Minute)
		l.wake(now)
	}
	if l.HTTPErrors != 0 {
		t.Errorf("push-back counted %d tries", l.HTTPErrors)
	}
}

func TestUnreachableInterfaceKeepsRetrying(t *testing.T) {
	l := &Link{}
	l.probing()
	now := start
	for i := 1; i <= 20; i++ {
		l.unreachable(now, "no route")
		if l.Status != LinkRetrying {
			t.Fatalf("after %d tries: %s, want retrying", i, l.Status)
		}
		l.wake(now.Add(29 * time.Second))
		if l.Status != LinkRetrying {
			t.Fatal("retried before the 30s pause was over")
		}
		now = now.Add(30 * time.Second)
		l.wake(now)
		l.probing()
	}
}

func TestResumingRetriesFailedLinksOnlyAfterGettingStuck(t *testing.T) {
	links := func() (retrying, failed, mismatch, needsPage *Link) {
		retrying = &Link{}
		retrying.probing()
		retrying.httpError(start, "HTTP 503")
		return retrying, &Link{Status: LinkFailed, Error: "gave up after 3 new links", Resolves: 3},
			&Link{Status: LinkMismatch, Error: "different file size"}, &Link{Status: LinkNeedsPage}
	}

	retrying, failed, mismatch, needsPage := links()
	for _, l := range []*Link{retrying, failed, mismatch, needsPage} {
		l.resume(false)
	}
	if retrying.Status != LinkPending || failed.Status != LinkFailed {
		t.Errorf("plain resume: retrying %s, failed %s; want pending, failed", retrying.Status, failed.Status)
	}

	retrying, failed, mismatch, needsPage = links()
	for _, l := range []*Link{retrying, failed, mismatch, needsPage} {
		l.resume(true)
	}
	if retrying.Status != LinkPending || failed.Status != LinkPending || failed.Resolves != 0 {
		t.Errorf("resume after getting stuck: retrying %s, failed %s (resolves %d); want both pending afresh", retrying.Status, failed.Status, failed.Resolves)
	}
	if mismatch.Status != LinkMismatch || needsPage.Status != LinkNeedsPage {
		t.Errorf("mismatch %s, needs page %s: resuming shouldn't touch them", mismatch.Status, needsPage.Status)
	}
}

func TestStuckLinks(t *testing.T) {
	for st, want := range map[LinkStatus]bool{
		LinkPending: false, LinkProbing: false, LinkReady: false, LinkResolving: false, LinkRetrying: false,
		LinkNeedsPage: true, LinkFailed: true, LinkMismatch: true,
	} {
		if got := (&Link{Status: st}).stuck(); got != want {
			t.Errorf("%s: stuck %v, want %v", st, got, want)
		}
	}
}

func TestRestoredLinkIsDueAtOnce(t *testing.T) {
	l := &Link{}
	l.probing()
	l.unreachable(start, "no route")
	l.restored()
	if l.Status != LinkPending {
		t.Fatalf("restored retrying link is %s, want pending", l.Status)
	}
}

func TestRetryingHoldsFromEveryLinkWaitingUntilOneIsReady(t *testing.T) {
	a, b := &Link{Status: LinkPending}, &Link{Status: LinkPending}
	d := &Download{running: true, st: State{Links: map[string]*Link{"a": a, "b": b}}}
	steps := []struct {
		a, b LinkStatus
		want bool
		why  string
	}{
		{LinkProbing, LinkPending, false, "a first probe holds its slot"},
		{LinkRetrying, LinkProbing, false, "one link still under way"},
		{LinkRetrying, LinkRetrying, true, "every link waits to retry"},
		{LinkProbing, LinkRetrying, true, "a retry's probe keeps it retrying"},
		{LinkResolving, LinkFailed, true, "so does a resolve"},
		{LinkReady, LinkRetrying, false, "a link is ready again"},
		{LinkProbing, LinkRetrying, false, "and a later probe doesn't change that"},
	}
	for _, s := range steps {
		a.Status, b.Status = s.a, s.b
		if got := d.Retrying(); got != s.want {
			t.Fatalf("%s/%s: Retrying %v, want %v (%s)", s.a, s.b, got, s.want, s.why)
		}
	}
	d.running = false
	if d.Retrying() {
		t.Fatal("a stopped download is not retrying")
	}
}

// A link that has fetched part of the file, with no landing page known, and
// gets an error page once (a VPN reconnect, a rate-limit page) tries again
// later. Only a page on every try makes it wait for a landing page.
func TestErrorPageOnALinkThatHadTheFileRetriesBeforeAskingForAPage(t *testing.T) {
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 0)}, main: "eth"}
	env.resolve = func(id, ifc, page string) { t.Errorf("resolved with no landing page") }
	url := "http://127.0.0.1/file"
	l := &Link{URL: url, Status: LinkPending, Bytes: 50}
	d := New(env, State{ID: "page", URL: url, Dir: t.TempDir(), Size: 100, Probed: true, Links: map[string]*Link{"eth": l}})
	usable := map[string]Iface{"eth": env.ifaces[0]}
	page := probeResult{status: 403, isPage: true}
	for i := 1; i <= maxHTTPErrors; i++ {
		l.probing()
		d.applyProbeLocked("eth", l, page, nil)
		d.decidePageHitsLocked(usable, "eth")
		want := LinkRetrying
		if i == maxHTTPErrors {
			want = LinkNeedsPage
		}
		if l.Status != want {
			t.Fatalf("page %d: %s (%s), want %s", i, l.Status, l.Error, want)
		}
	}
}
