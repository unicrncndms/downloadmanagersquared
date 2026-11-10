package download

import (
	"context"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"downloadmanagersquared/engine/internal/archive"
)

type probeResult struct {
	status  int
	size    int64 // -1 when unknown
	rangeOK bool
	isPage  bool // the server sent an HTML page, not the file
	name    string
	version Version
	wait    time.Duration // how long a busy server asked us to stay away
	dups    []string      // mirrors the server named (Link: rel=duplicate)
	family  string        // "IPv4" or "IPv6": what the answer came over
}

// busy: the server pushed back. That says nothing about the link itself.
func (r probeResult) busy() bool { return serverBusy(r.status) }

// serverBusy: statuses that mean "come back later".
func serverBusy(status int) bool { return status == 429 || status == 503 }

// rejected means this link is no good for this IP (expired, IP-bound or a landing page).
func (r probeResult) rejected() bool { return !r.busy() && (linkRejected(r.status) || r.isPage) }

// linkRejected: statuses that mean the link itself is not valid for this IP.
func linkRejected(status int) bool {
	switch status {
	case 401, 403, 404, 410:
		return true
	}
	return false
}

func newRequest(ctx context.Context, l Link, ua string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.URL, nil)
	if err != nil {
		return nil, err
	}
	if l.Cookie != "" {
		req.Header.Set("Cookie", string(l.Cookie))
	}
	if l.Referer != "" {
		req.Header.Set("Referer", l.Referer)
	}
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	req.Header.Set("Accept-Encoding", "identity")
	return req, nil
}

// probeLink asks for the first byte only: enough to learn size, range support and name.
func probeLink(ctx context.Context, client *http.Client, l Link, ua string) (probeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	req, err := newRequest(ctx, l, ua)
	if err != nil {
		return probeResult{}, err
	}
	req.Header.Set("Range", "bytes=0-0")
	var family string // of the last connection, the one the answer came over
	req = req.WithContext(httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) {
		family = "IPv6"
		if a, ok := i.Conn.RemoteAddr().(*net.TCPAddr); !ok || a.IP.To4() != nil {
			family = "IPv4"
		}
	}}))
	resp, err := client.Do(req)
	if err != nil {
		return probeResult{}, err
	}
	defer resp.Body.Close()

	r := probeResult{status: resp.StatusCode, size: -1, family: family}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	r.isPage = ct == "text/html" || ct == "application/xhtml+xml"
	switch resp.StatusCode {
	case http.StatusPartialContent:
		if _, total, ok := parseContentRange(resp.Header.Get("Content-Range")); ok && total >= 0 {
			r.size, r.rangeOK = total, true
		}
	case http.StatusOK:
		r.size = resp.ContentLength
	}
	if r.status == http.StatusOK || r.status == http.StatusPartialContent {
		r.version = versionOf(resp.Header)
		r.dups = duplicates(resp.Header.Values("Link"), resp.Request.URL)
	}
	r.name = remoteName(resp)
	if r.busy() {
		r.wait = retryAfter(resp.Header.Get("Retry-After"), time.Now())
	}
	return r, nil
}

// parseContentRange reads "bytes START-END/TOTAL"; total is -1 for "*".
func parseContentRange(s string) (start, total int64, ok bool) {
	s, found := strings.CutPrefix(strings.TrimSpace(s), "bytes ")
	if !found {
		return 0, 0, false
	}
	rng, tot, found := strings.Cut(s, "/")
	if !found {
		return 0, 0, false
	}
	first, _, found := strings.Cut(rng, "-")
	if !found {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	if tot == "*" {
		return start, -1, true
	}
	total, err = strconv.ParseInt(tot, 10, 64)
	return start, total, err == nil
}

func remoteName(resp *http.Response) string {
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil && params["filename"] != "" {
		return SanitizeFilename(params["filename"])
	}
	if resp.Request != nil && resp.Request.URL != nil {
		base := path.Base(resp.Request.URL.Path)
		if u, err := url.PathUnescape(base); err == nil {
			base = u
		}
		if base != "/" && base != "." {
			return SanitizeFilename(base)
		}
	}
	return ""
}

// SanitizeFilename makes a name safe to create on Windows, by the same rules
// as a Download as folder's entries. "" stays "": the name is still unknown.
func SanitizeFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, name)
	if name = strings.Trim(name, " ."); name == "" {
		return ""
	}
	name = archive.CleanPart(name)
	if len(name) > 200 {
		ext := path.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		name = strings.ToValidUTF8(name[:200-len(ext)], "") + ext // never half a character
	}
	return name
}

type errKind int

const (
	kindNet     errKind = iota
	kindExpired         // link rejected: needs a new probe or resolve
	kindBusy            // 429/503: the server pushed back
	kindRange           // server does not honor ranges on this link
	kindChanged         // the file on the server is no longer the one being downloaded
	kindDisk
	kindHTTP // any other error status
)

type fetchError struct {
	kind       errKind
	msg        string
	retryAfter time.Duration
}

func (e *fetchError) Error() string { return e.msg }

func statusError(resp *http.Response) *fetchError {
	if linkRejected(resp.StatusCode) {
		return &fetchError{kind: kindExpired, msg: "link rejected: " + resp.Status}
	}
	if serverBusy(resp.StatusCode) {
		return &fetchError{kind: kindBusy, msg: "server busy: " + resp.Status, retryAfter: retryAfter(resp.Header.Get("Retry-After"), time.Now())}
	}
	if resp.StatusCode >= 400 {
		return &fetchError{kind: kindHTTP, msg: fmt.Sprintf("HTTP %s", resp.Status)}
	}
	return nil
}

func (d *Download) probe(ctx context.Context, ifc Iface, l Link) {
	res, err := probeLink(ctx, ifc.Client, l, d.st.UserAgent)
	usable := map[string]Iface{}
	all := d.env.Paths(d.VPN())
	for _, u := range all {
		usable[u.ID] = u
	}
	mainID := mainOf(all)
	d.mu.Lock()
	delete(d.probing, ifc.ID)
	cur := d.st.Links[ifc.ID]
	if cur == nil || cur.URL != l.URL || cur.Status != LinkProbing {
		d.mu.Unlock()
		return
	}
	if ctx.Err() != nil {
		cur.requeue()
		d.mu.Unlock()
		return
	}
	if err == nil && ifc.ByFamily != nil {
		cur.Family = res.family // a new probe chooses again
	}
	if err == nil && !res.rejected() && res.status < 400 && ifc.ID != mainID && d.mainMayAnswerLocked(usable, mainID) {
		cur.hold(time.Now(), res) // the main interface decides what the file is
	} else {
		d.applyProbeLocked(ifc.ID, cur, res, err)
	}
	d.mu.Unlock()
	d.env.Changed(d)
}

func (d *Download) applyProbeLocked(ifaceID string, l *Link, r probeResult, err error) {
	defer d.leaveMirrorLocked(ifaceID, l)
	mirror := d.mirrorLocked(l.URL) != nil
	if err != nil {
		log.Printf("download %s probe via %s: %s", d.st.ID, ifaceID, shortErr(err))
		l.unreachable(time.Now(), shortErr(err))
		return
	}
	log.Printf("download %s probe via %s: HTTP %d, size %d, ranges %v, page %v", d.st.ID, ifaceID, r.status, r.size, r.rangeOK, r.isPage)
	if r.busy() {
		now := time.Now()
		d.ifRun(ifaceID).backoff.pushedBack(now, r.wait)
		l.serverBusy(now, r.wait, fmt.Sprintf("server busy: HTTP %d", r.status))
		return
	}
	if r.rejected() {
		reason := fmt.Sprintf("link not valid on this connection (HTTP %d)", r.status)
		if r.isPage {
			reason = "link leads to a web page, not the file"
		}
		switch {
		case mirror:
			l.fail(reason) // a mirror has no landing page: the next mirror is tried
		case r.isPage && d.st.PageURL == "" && l.URL == d.st.URL:
			// Either the link is a landing page, or it is the file link and this
			// interface got an error page for it. Decide once every interface has answered.
			l.webPage()
		default:
			d.needResolveLocked(ifaceID, l, reason)
		}
		return
	}
	if r.status >= 400 {
		l.httpError(time.Now(), fmt.Sprintf("HTTP %d", r.status))
		return
	}
	if why := d.mirrorMismatchLocked(r); mirror && why != "" {
		l.mismatch(why)
		return
	}
	if !d.st.Probed {
		d.st.Size, d.st.RangeOK, d.st.RemoteName, d.st.Probed = r.size, r.rangeOK && r.size > 0, r.name, true
		d.st.Version = r.version
		if d.st.Filename == "" {
			d.st.Filename = r.name
		}
		if d.st.Filename == "" {
			d.st.Filename = "download"
		}
		d.folderProbedLocked()
		if len(d.st.Mirrors) == 0 && len(r.dups) > 0 && r.version.hashed() {
			// Only with the server's checksum: it is what tells the mirrors' copies apart.
			d.addMirrorsLocked(ifaceID, r.dups)
		}
	} else {
		if r.size != d.st.Size {
			l.mismatch(fmt.Sprintf("different file size (%d vs %d bytes)", r.size, d.st.Size))
			return
		}
		if !mirror && d.st.RemoteName != "" && r.name != "" && !strings.EqualFold(r.name, d.st.RemoteName) {
			l.mismatch(fmt.Sprintf("different file name (%q vs %q)", r.name, d.st.RemoteName))
			return
		}
		if d.st.RangeOK && !r.rangeOK {
			l.fail("this link does not allow split downloading")
			return
		}
		if d.st.RangeOK && !mirror && !r.version.same(d.st.Version) {
			l.mismatch(versionMsg) // its bytes don't belong with the ones on disk
			return
		}
	}
	l.Version = r.version
	l.ready()
}

// decidePageHitsLocked settles interfaces that got a web page for the source URL.
// If another interface got the file, the link is the file and those interfaces
// need their own link from the landing page. Only if no interface got the file is
// the link itself the landing page.
func (d *Download) decidePageHitsLocked(usable map[string]Iface) {
	var hits []string
	for id, l := range d.st.Links {
		if l.pageHit {
			hits = append(hits, id)
		}
	}
	if len(hits) == 0 || len(d.probing) > 0 {
		return
	}
	if !d.st.Probed && d.st.PageURL == "" {
		for id := range usable {
			if l := d.st.Links[id]; l == nil || l.answering() {
				return // still waiting on an interface that might get the file
			}
		}
		d.st.PageURL = d.st.URL
	}
	for _, id := range hits {
		l := d.st.Links[id]
		d.needResolveLocked(id, l, "this connection gets a web page instead of the file")
	}
}

// mainMayAnswerLocked: the main interface's probe is still to come, so answers
// from the other interfaces wait for it.
func (d *Download) mainMayAnswerLocked(usable map[string]Iface, mainID string) bool {
	if d.st.Probed {
		return false
	}
	if _, ok := usable[mainID]; !ok {
		return false
	}
	l := d.st.Links[mainID]
	return l == nil || l.answering()
}

// settleHeldLocked applies held answers once the main interface has decided the
// file, or can't. In the second case the earliest answer decides it.
func (d *Download) settleHeldLocked(usable map[string]Iface, mainID string) {
	if d.mainMayAnswerLocked(usable, mainID) {
		return
	}
	var ids []string
	for id, l := range d.st.Links {
		if l.held != nil {
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, func(a, b string) int { return d.st.Links[a].heldAt.Compare(d.st.Links[b].heldAt) })
	for _, id := range ids {
		l := d.st.Links[id]
		d.applyProbeLocked(id, l, *l.takeHeld(), nil)
	}
}

func (d *Download) needResolveLocked(ifaceID string, l *Link, reason string) {
	if l.needResolve(d.st.PageURL != "", reason) {
		id, page, browser := d.st.ID, d.st.PageURL, d.st.Browser
		go d.env.RequestResolve(id, ifaceID, page, browser)
	}
}
