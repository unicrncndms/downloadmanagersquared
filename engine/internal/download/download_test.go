package download

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/paths"
)

// tagTransport stands in for an interface: it marks each request with the
// interface it "left" through and can slow the response down.
type tagTransport struct {
	tag     string
	delayKB time.Duration // pause per 64 KiB read
}

func (t tagTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Iface", t.tag)
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err == nil && t.delayKB > 0 {
		resp.Body = slowBody{resp.Body, t.delayKB}
	}
	return resp, err
}

type slowBody struct {
	io.ReadCloser
	d time.Duration
}

func (s slowBody) Read(p []byte) (int, error) {
	if len(p) > 64<<10 {
		p = p[:64<<10]
	}
	time.Sleep(s.d)
	return s.ReadCloser.Read(p)
}

type testEnv struct {
	ifaces   []Iface
	main     string
	asked    atomic.Value // the VPN mode Paths was last asked for
	resolve  func(id, iface, page string)
	finished chan struct{}
}

func (e *testEnv) Paths(m paths.Mode) []Iface {
	e.asked.Store(m)
	var out []Iface // the main path first
	for _, i := range e.ifaces {
		if i.ID == e.main {
			out = append([]Iface{i}, out...)
		} else {
			out = append(out, i)
		}
	}
	return out
}

func (e *testEnv) ETAWindow() time.Duration { return 10 * time.Second }
func (e *testEnv) Changed(*Download)        {}
func (e *testEnv) Finished(*Download)       { close(e.finished) }
func (e *testEnv) RequestResolve(id, iface, page string) {
	if e.resolve != nil {
		e.resolve(id, iface, page)
	}
}

func iface(tag string, conns int, delay time.Duration) Iface {
	return Iface{ID: tag, Conns: conns, Client: &http.Client{Transport: tagTransport{tag, delay}}}
}

func randomData(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(1)).Read(b)
	return b
}

// fileServer serves data at /file/<iface> and only to that interface,
// like a hoster that binds each generated link to one IP.
func fileServer(t *testing.T, data []byte, bytesBy map[string]*atomic.Int64) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html>landing page</html>")
	})
	mux.HandleFunc("/file/", func(w http.ResponseWriter, r *http.Request) {
		owner := strings.TrimPrefix(r.URL.Path, "/file/")
		if owner != "any" && owner != r.Header.Get("X-Iface") {
			http.Error(w, "link bound to another IP", http.StatusForbidden)
			return
		}
		if c := bytesBy[r.Header.Get("X-Iface")]; c != nil && r.Header.Get("Range") != "bytes=0-0" {
			w = countingWriter{w, c}
		}
		w.Header().Set("Content-Disposition", `attachment; filename="big.bin"`)
		http.ServeContent(w, r, "big.bin", time.Time{}, bytes.NewReader(data))
	})
	return httptest.NewServer(mux)
}

type countingWriter struct {
	http.ResponseWriter
	n *atomic.Int64
}

func (c countingWriter) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	c.n.Add(int64(n))
	return n, err
}

func runToEnd(t *testing.T, env *testEnv, st State) *Download {
	t.Helper()
	env.finished = make(chan struct{})
	d := New(env, st)
	d.Start()
	select {
	case <-env.finished:
	case <-time.After(60 * time.Second):
		t.Fatalf("download did not finish: %+v", d.Summary())
	}
	return d
}

func checkFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file content differs (got %d bytes, want %d)", len(got), len(want))
	}
}

func TestSplitAcrossInterfacesWithIPBoundLinks(t *testing.T) {
	data := randomData(64 << 20)
	counts := map[string]*atomic.Int64{"eth": {}, "wifi": {}}
	srv := fileServer(t, data, counts)
	defer srv.Close()
	dir := t.TempDir()

	var resolveMu sync.Mutex
	var resolved []string
	env := &testEnv{
		// eth ~4x faster than wifi (per-read delays slow each connection)
		ifaces: []Iface{iface("eth", 4, 8*time.Millisecond), iface("wifi", 2, 16*time.Millisecond)},
		main:   "eth",
	}
	var d *Download
	env.resolve = func(id, ifc, page string) {
		// What the browser extension does: open the page via that interface, report its link.
		resolveMu.Lock()
		resolved = append(resolved, ifc)
		resolveMu.Unlock()
		if page != srv.URL+"/page" {
			t.Errorf("resolve got page %q", page)
		}
		time.Sleep(100 * time.Millisecond)
		d.SetResolved(ifc, srv.URL+"/file/"+ifc, "", page)
	}
	sum := sha256.Sum256(data)
	st := State{
		ID: "t1", URL: srv.URL + "/file/eth", PageURL: srv.URL + "/page", Dir: dir,
		Size: -1, Status: StatusQueued, SHA256: hex.EncodeToString(sum[:]),
	}
	env.finished = make(chan struct{})
	d = New(env, st)
	d.Start()
	select {
	case <-env.finished:
	case <-time.After(60 * time.Second):
		t.Fatalf("timeout: %+v", d.Summary())
	}

	s := d.Summary()
	if s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
	if len(resolved) != 1 || resolved[0] != "wifi" {
		t.Errorf("expected exactly one resolve for wifi, got %v", resolved)
	}
	eth, wifi := counts["eth"].Load(), counts["wifi"].Load()
	t.Logf("eth sent %d MiB, wifi sent %d MiB", eth>>20, wifi>>20)
	if wifi == 0 || eth == 0 {
		t.Errorf("both interfaces should carry data (eth=%d wifi=%d)", eth, wifi)
	}
	if eth <= wifi {
		t.Errorf("the faster interface should carry more (eth=%d wifi=%d)", eth, wifi)
	}
	// Splits cut connections short, so a little in-flight data is wasted, but not much.
	if waste := eth + wifi - int64(len(data)); waste > int64(len(data))/5 {
		t.Errorf("too much data transferred twice: %d MiB extra", waste>>20)
	}
}

func TestMismatchedLinkIsExcluded(t *testing.T) {
	data := randomData(8 << 20)
	other := randomData(9 << 20)
	srv := fileServer(t, data, nil)
	defer srv.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="big.bin"`)
		http.ServeContent(w, r, "big.bin", time.Time{}, bytes.NewReader(other))
	})
	wrong := httptest.NewServer(mux)
	defer wrong.Close()

	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 0), iface("wifi", 2, 0)}, main: "eth"}
	var d *Download
	env.resolve = func(id, ifc, page string) { d.SetResolved(ifc, wrong.URL+"/x", "", "") }
	env.finished = make(chan struct{})
	d = New(env, State{ID: "t2", URL: srv.URL + "/file/eth", PageURL: srv.URL + "/page", Dir: dir, Size: -1})
	d.Start()
	select {
	case <-env.finished:
	case <-time.After(30 * time.Second):
		t.Fatalf("timeout: %+v", d.Summary())
	}
	s := d.Summary()
	if s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	if s.Links["wifi"].Status != LinkMismatch {
		t.Errorf("wifi link should be a mismatch, got %+v", s.Links["wifi"])
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
}

func TestNoRangeUsesMainOnly(t *testing.T) {
	data := randomData(3 << 20)
	var served sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "3145728")
		if r.Header.Get("Range") == "" {
			served.Store(r.Header.Get("X-Iface"), true)
		}
		w.Write(data) // ignores Range
	}))
	defer srv.Close()
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 4, 0), iface("wifi", 4, 0)}, main: "wifi"}
	d := runToEnd(t, env, State{ID: "t3", URL: srv.URL + "/thing.iso", Dir: dir, Size: -1})
	s := d.Summary()
	if s.Status != StatusCompleted || s.RangeOK {
		t.Fatalf("status %s rangeOk %v: %s", s.Status, s.RangeOK, s.Error)
	}
	if _, ok := served.Load("eth"); ok {
		t.Errorf("non-main interface downloaded the file")
	}
	if _, ok := served.Load("wifi"); !ok {
		t.Errorf("main interface did not download the file")
	}
	checkFile(t, filepath.Join(dir, "thing.iso"), data)
}

func TestUnknownSizeEndsWithTheResponse(t *testing.T) {
	data := randomData(3<<20 + 123)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Flushing before the end means no Content-Length: the size is unknown until EOF.
		for b := data; len(b) > 0; {
			n := min(len(b), 64<<10)
			w.Write(b[:n])
			w.(http.Flusher).Flush()
			b = b[n:]
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 4, 0)}, main: "eth"}
	d := runToEnd(t, env, State{ID: "t9", URL: srv.URL + "/stream.bin", Dir: dir, Size: -1})
	if s := d.Summary(); s.Status != StatusCompleted || s.RangeOK || s.Size != -1 {
		t.Fatalf("status %s rangeOk %v size %d: %s", s.Status, s.RangeOK, s.Size, s.Error)
	}
	checkFile(t, filepath.Join(dir, "stream.bin"), data)
}

func TestPauseAndResume(t *testing.T) {
	data := randomData(24 << 20)
	srv := fileServer(t, data, nil)
	defer srv.Close()
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("any", 3, 3*time.Millisecond)}, main: "any", finished: make(chan struct{})}
	d := New(env, State{ID: "t4", URL: srv.URL + "/file/any", Dir: dir, Size: -1})
	d.Start()
	for d.Summary().Done < 4<<20 {
		time.Sleep(50 * time.Millisecond)
	}
	d.Stop(StatusPaused)
	st := d.State()
	if st.Status != StatusPaused || len(st.Remaining) == 0 {
		t.Fatalf("after pause: status %s, remaining %v", st.Status, st.Remaining)
	}

	// Simulate an engine restart: a fresh Download from the saved state.
	env.ifaces = []Iface{iface("any", 3, 0)}
	st.Status = StatusQueued
	d2 := runToEnd(t, env, st)
	if s := d2.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
}

// A server that refuses connections beyond a per-IP limit with 403 must not
// make the engine throw the link away while other connections still work.
func TestConnectionLimit403IsNotExpiry(t *testing.T) {
	data := randomData(32 << 20)
	var open atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if open.Add(1) > 2 {
			open.Add(-1)
			http.Error(w, "too many connections", http.StatusForbidden)
			return
		}
		defer open.Add(-1)
		http.ServeContent(w, r, "big.bin", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 6, 4*time.Millisecond)}, main: "eth"}
	resolves := 0
	env.resolve = func(string, string, string) { resolves++ }
	d := runToEnd(t, env, State{ID: "t5", URL: srv.URL + "/big.bin", PageURL: srv.URL + "/page", Dir: dir, Size: -1})
	if s := d.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	if resolves != 0 {
		t.Errorf("asked the browser for a new link %d times; a connection limit is not an expired link", resolves)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
}

// When extra connections get 429, the ones already working must keep going.
func TestPushBackKeepsWorkingConnections(t *testing.T) {
	data := randomData(32 << 20)
	var open atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if open.Add(1) > 2 {
			open.Add(-1)
			w.Header().Set("Retry-After", "600")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		defer open.Add(-1)
		http.ServeContent(w, r, "big.bin", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 4, 2*time.Millisecond)}, main: "eth"}
	start := time.Now()
	d := runToEnd(t, env, State{ID: "t6", URL: srv.URL + "/big.bin", Dir: dir, Size: -1})
	if s := d.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	if el := time.Since(start); el > 30*time.Second {
		t.Errorf("took %v: working connections waited out the 600s Retry-After", el)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
}

// htmlForOtherIPs serves the file only to the interface named in the path and an
// HTML error page (status 200) to every other one, like many hosters do.
func htmlForOtherIPs(data []byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/page" || strings.TrimPrefix(r.URL.Path, "/file/") != r.Header.Get("X-Iface") {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<html>not for your IP</html>")
			return
		}
		http.ServeContent(w, r, "big.bin", time.Time{}, bytes.NewReader(data))
	}))
}

// The case from real use: the browser handed over the file link but no landing
// page. Wi-Fi gets an HTML error page for Ethernet's link. That must not make the
// file link the "landing page"; Wi-Fi waits until a landing page is set.
func TestHTMLErrorPageDoesNotMakeFileLinkTheLandingPage(t *testing.T) {
	data := randomData(16 << 20)
	srv := htmlForOtherIPs(data)
	defer srv.Close()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 2*time.Millisecond), iface("wifi", 2, 2*time.Millisecond)}, main: "eth"}
	var mu sync.Mutex
	var pages []string
	var d *Download
	env.resolve = func(id, ifc, page string) {
		mu.Lock()
		pages = append(pages, page)
		mu.Unlock()
		d.SetResolved(ifc, srv.URL+"/file/"+ifc, "", page)
	}
	env.finished = make(chan struct{})
	d = New(env, State{ID: "t7", URL: srv.URL + "/file/eth", Dir: t.TempDir(), Size: -1})
	d.Start()

	deadline := time.Now().Add(10 * time.Second)
	for d.Summary().Links["wifi"].Status != LinkNeedsPage && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if d.Summary().Links["wifi"].Status != LinkNeedsPage {
		t.Fatalf("wifi should wait for a landing page, got %+v", d.Summary().Links["wifi"])
	}
	mu.Lock()
	if len(pages) != 0 {
		t.Fatalf("resolved with page(s) %v before a landing page was known", pages)
	}
	mu.Unlock()

	d.SetPageURL(srv.URL + "/page")
	select {
	case <-env.finished:
	case <-time.After(30 * time.Second):
		t.Fatalf("timeout: %+v", d.Summary())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(pages) != 1 || pages[0] != srv.URL+"/page" {
		t.Errorf("wifi should resolve once via the landing page, got %v", pages)
	}
	if s := d.Summary(); s.Status != StatusCompleted || !s.Links["wifi"].Resolved {
		t.Errorf("status %s, wifi %+v", s.Status, s.Links["wifi"])
	}
}

// A landing page given as the download link (every connection gets HTML) is
// recognized as the landing page and every connection resolves from it.
func TestLandingPageAsLinkResolvesEverywhere(t *testing.T) {
	data := randomData(8 << 20)
	srv := htmlForOtherIPs(data)
	defer srv.Close()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 0), iface("wifi", 2, 0)}, main: "eth"}
	var mu sync.Mutex
	got := map[string]string{}
	var d *Download
	env.resolve = func(id, ifc, page string) {
		mu.Lock()
		got[ifc] = page
		mu.Unlock()
		d.SetResolved(ifc, srv.URL+"/file/"+ifc, "", page)
	}
	env.finished = make(chan struct{})
	d = New(env, State{ID: "t8", URL: srv.URL + "/page", Dir: t.TempDir(), Size: -1})
	d.Start()
	select {
	case <-env.finished:
	case <-time.After(30 * time.Second):
		t.Fatalf("timeout: %+v", d.Summary())
	}
	mu.Lock()
	defer mu.Unlock()
	if got["eth"] != srv.URL+"/page" || got["wifi"] != srv.URL+"/page" {
		t.Errorf("both connections should resolve from the page, got %v", got)
	}
	if s := d.Summary(); s.Status != StatusCompleted {
		t.Errorf("status %s: %s", s.Status, s.Error)
	}
}

// Wi-Fi gets a web page for the source URL while Ethernet is still answering.
// Until Ethernet has answered nobody knows what that page means, so Wi-Fi's link
// is still probing, not failed.
func TestUndecidedWebPageIsStillProbing(t *testing.T) {
	data := randomData(4 << 20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Iface") != "eth" {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<html>not for your IP</html>")
			return
		}
		time.Sleep(time.Second)
		http.ServeContent(w, r, "big.bin", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 0), iface("wifi", 2, 0)}, main: "eth"}
	env.finished = make(chan struct{})
	d := New(env, State{ID: "t9", URL: srv.URL + "/file", Dir: t.TempDir(), Size: -1})
	d.Start()

	time.Sleep(600 * time.Millisecond)
	if l := d.Summary().Links["wifi"]; l.Status != LinkProbing {
		t.Errorf("before Ethernet answered, wifi is %+v, want probing", l)
	}
	select {
	case <-env.finished:
	case <-time.After(30 * time.Second):
		t.Fatalf("timeout: %+v", d.Summary())
	}
	if l := d.Summary().Links["wifi"]; l.Status != LinkNeedsPage {
		t.Errorf("once Ethernet got the file, wifi is %+v, want needs-page", l)
	}
}

// Every interface needs its own link and the browser can't get one: there is
// nothing left to try, so the download pauses. Resuming tries again.
func TestPausesWhenNoInterfaceCanReachTheFile(t *testing.T) {
	srv := htmlForOtherIPs(randomData(1 << 20))
	defer srv.Close()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 0), iface("wifi", 2, 0)}, main: "eth"}
	var resolves atomic.Int32
	var d *Download
	env.resolve = func(id, ifc, page string) {
		resolves.Add(1)
		d.ResolveFailed(ifc, "no download button found")
	}
	d = New(env, State{ID: "t10", URL: srv.URL + "/file", PageURL: srv.URL + "/page", Dir: t.TempDir(), Size: -1})
	waitPaused := func() {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for (d.Running() || d.Status() != StatusPaused) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if s := d.Summary(); s.Status != StatusPaused || !strings.Contains(s.Error, "no connection can reach the file") {
			t.Fatalf("status %s (%q), want paused because no connection can reach the file", s.Status, s.Error)
		}
	}
	d.Start()
	waitPaused()
	if n := resolves.Load(); n != 2 {
		t.Fatalf("%d resolves, want one per interface", n)
	}
	d.Start()
	waitPaused()
	if n := resolves.Load(); n != 4 {
		t.Fatalf("%d resolves after resuming, want each interface tried again", n)
	}
}

// A hotel Wi-Fi serves something else and answers first. The main interface's
// answer still decides what the file is, so Wi-Fi is the mismatch.
func TestMainInterfaceDecidesWhatTheFileIs(t *testing.T) {
	data := randomData(4 << 20)
	wrong := randomData(1 << 20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Iface") == "wifi" {
			http.ServeContent(w, r, "portal.bin", time.Time{}, bytes.NewReader(wrong))
			return
		}
		if r.Header.Get("Range") == "bytes=0-0" {
			time.Sleep(300 * time.Millisecond)
		}
		http.ServeContent(w, r, "big.bin", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 0), iface("wifi", 2, 0)}, main: "eth"}
	d := runToEnd(t, env, State{ID: "t11", URL: srv.URL + "/big.bin", Filename: "big.bin", Dir: dir, Size: -1})
	s := d.Summary()
	if s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	if s.Links["wifi"].Status != LinkMismatch || s.Links["eth"].Status != LinkReady {
		t.Errorf("eth %+v, wifi %+v: wifi should be the mismatch", s.Links["eth"], s.Links["wifi"])
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
}

// waitFinished is runToEnd for a download that is already started, with a deadline.
func waitFinished(t *testing.T, env *testEnv, d *Download, within time.Duration) {
	t.Helper()
	select {
	case <-env.finished:
	case <-time.After(within):
		t.Fatalf("download did not finish within %v: %+v", within, d.Summary())
	}
}

// A busy server during the probe says when to come back; the link waits that
// long, not a minute, and it costs none of its five tries.
func TestBusyServerDuringProbeIsWaitedOut(t *testing.T) {
	data := randomData(2 << 20)
	var probes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" && probes.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "<html>busy</html>")
			return
		}
		http.ServeContent(w, r, "big.bin", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 0)}, main: "eth", finished: make(chan struct{})}
	d := New(env, State{ID: "t12", URL: srv.URL + "/big.bin", Dir: dir, Size: -1})
	d.Start()
	deadline := time.Now().Add(900 * time.Millisecond)
	for !d.Summary().Links["eth"].Busy && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if l := d.Summary().Links["eth"]; !l.Busy || l.Allowed != 1 || l.Status != LinkRetrying {
		t.Errorf("during the server's pause eth is %+v, want busy, retrying, 1 connection allowed", l)
	}
	waitFinished(t, env, d, 15*time.Second)
	if s := d.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	if n := d.State().Links["eth"].HTTPErrors; n != 0 {
		t.Errorf("push-back counted as %d tries", n)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
}

// One HTTP error on one connection is a blip: the interface carries on.
func TestOneHTTPErrorDoesNotStopTheInterface(t *testing.T) {
	data := randomData(16 << 20)
	var failed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=0-0" && failed.CompareAndSwap(false, true) {
			http.Error(w, "oops", http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, "big.bin", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 4, time.Millisecond)}, main: "eth", finished: make(chan struct{})}
	d := New(env, State{ID: "t13", URL: srv.URL + "/big.bin", Dir: dir, Size: -1})
	d.Start()
	waitFinished(t, env, d, 20*time.Second)
	if s := d.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	if !failed.Load() {
		t.Fatal("the server never sent its error")
	}
	if n := d.State().Links["eth"].HTTPErrors; n != 0 {
		t.Errorf("one blip counted as %d tries", n)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
}

func TestPauseForVPNWaitsUntilStartedAgain(t *testing.T) {
	env := &testEnv{} // no path: the download idles until paused
	d := New(env, State{ID: "v1", URL: "https://example.com/big.iso", Dir: t.TempDir(), Size: -1,
		Status: StatusQueued, VPN: paths.VPNOnly, Links: map[string]*Link{}})
	d.Start()
	waitAsked(t, env)
	if m, _ := env.asked.Load().(paths.Mode); m != paths.VPNOnly {
		t.Fatalf("asked for paths under %q, want VPN only", m)
	}
	d.PauseForVPN()
	if d.Status() != StatusPaused || !d.WaitingForVPN() || !strings.Contains(d.Summary().Error, "VPN") {
		t.Fatalf("%q with %q, waiting for VPN %v", d.Status(), d.Summary().Error, d.WaitingForVPN())
	}
	d.Start()
	defer d.Stop(StatusPaused)
	if d.WaitingForVPN() {
		t.Fatal("still waiting for VPN once started")
	}
}

func TestPauseForVPNLeavesAPausedDownloadAlone(t *testing.T) {
	d := New(&testEnv{}, State{ID: "v2", URL: "https://example.com/big.iso", Dir: t.TempDir(), Size: -1,
		Status: StatusPaused, VPN: paths.VPNOnly, Links: map[string]*Link{}})
	d.PauseForVPN()
	if d.WaitingForVPN() {
		t.Fatal("a download the user paused would resume by itself")
	}
}

func waitAsked(t *testing.T, env *testEnv) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for env.asked.Load() == nil {
		if time.Now().After(deadline) {
			t.Fatal("never asked for its paths")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
