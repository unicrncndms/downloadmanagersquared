package download

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type probeResult struct {
	status  int
	size    int64 // -1 when unknown
	rangeOK bool
	isPage  bool // the server sent an HTML page, not the file
	name    string
	wait    time.Duration // how long a busy server asked us to stay away
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
		req.Header.Set("Cookie", l.Cookie)
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
	resp, err := client.Do(req)
	if err != nil {
		return probeResult{}, err
	}
	defer resp.Body.Close()

	r := probeResult{status: resp.StatusCode, size: -1}
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

// SanitizeFilename makes a name safe to create on Windows.
func SanitizeFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if len(name) > 200 {
		ext := path.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		name = name[:200-len(ext)] + ext
	}
	switch strings.ToUpper(strings.TrimSuffix(name, path.Ext(name))) {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "LPT1", "LPT2", "LPT3":
		name = "_" + name
	}
	return name
}

type errKind int

const (
	kindNet     errKind = iota
	kindExpired         // link rejected: needs a new probe or resolve
	kindBusy            // 429/503: the server pushed back
	kindRange           // server does not honor ranges on this link
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
