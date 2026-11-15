package download

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A fake hoster that counts what it is asked, per path, split into probes
// (Range 0-0) and data GETs. These tests pin the requests the engine causes
// per event (ADR 0010), so a later change can't raise them unnoticed.
type countingHoster struct {
	*httptest.Server
	mu     sync.Mutex
	probes map[string]int
	data   map[string]int
}

// "/page" serves HTML; everything else serves the file, but refuseData(path)
// answers data GETs (not probes) with 403.
func newCountingHoster(t *testing.T, file []byte, refuseData func(path string) bool) *countingHoster {
	c := &countingHoster{probes: map[string]int{}, data: map[string]int{}}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probe := r.Header.Get("Range") == "bytes=0-0"
		c.mu.Lock()
		if probe {
			c.probes[r.URL.Path]++
		} else {
			c.data[r.URL.Path]++
		}
		c.mu.Unlock()
		if r.URL.Path == "/page" {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<html>landing page</html>")
			return
		}
		if !probe && refuseData != nil && refuseData(r.URL.Path) {
			http.Error(w, "link expired", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="big.bin"`)
		http.ServeContent(w, r, "big.bin", time.Time{}, bytes.NewReader(file))
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *countingHoster) count(path string) (probes, data int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.probes[path], c.data[path]
}

func finish(t *testing.T, env *testEnv, d *Download) {
	t.Helper()
	d.Start()
	t.Cleanup(func() { d.Stop(StatusPaused) })
	select {
	case <-env.finished:
	case <-time.After(30 * time.Second):
		t.Fatalf("timeout: %+v", d.Summary())
	}
}

// Start: one probe of the link, no resolve, then data.
func TestRequestsAtStart(t *testing.T) {
	h := newCountingHoster(t, randomData(1<<20), nil)
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 0)}, main: "eth", finished: make(chan struct{})}
	env.resolve = func(id, ifc, page string) { t.Errorf("unexpected resolve") }
	finish(t, env, New(env, State{ID: "start", URL: h.URL + "/file", Dir: t.TempDir(), Size: -1}))
	if p, _ := h.count("/file"); p != 1 {
		t.Fatalf("%d probes at start, want 1", p)
	}
}

// Expiry with a known landing page: the old link is not probed again, one
// resolve, one probe of the new link.
func TestRequestsOnExpiryWithLandingPage(t *testing.T) {
	h := newCountingHoster(t, randomData(1<<20), func(p string) bool { return p == "/old" })
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 0)}, main: "eth", finished: make(chan struct{})}
	var d *Download
	var resolves atomic.Int32
	env.resolve = func(id, ifc, page string) {
		resolves.Add(1)
		go d.SetResolved(ifc, h.URL+"/good", "", page)
	}
	d = New(env, State{ID: "expiry", URL: h.URL + "/old", PageURL: h.URL + "/page", Dir: t.TempDir(), Size: -1})
	finish(t, env, d)
	if p, _ := h.count("/old"); p != 1 {
		t.Fatalf("%d probes of the old link, want 1 (the start's)", p)
	}
	if p, _ := h.count("/good"); p != 1 {
		t.Fatalf("%d probes of the new link, want 1", p)
	}
	if n := resolves.Load(); n != 1 {
		t.Fatalf("%d resolves, want 1", n)
	}
}

// A pasted landing page is probed on one interface, then each interface
// resolves once and probes its own link once.
func TestRequestsForPastedLandingPage(t *testing.T) {
	h := newCountingHoster(t, randomData(1<<20), nil)
	env := &testEnv{ifaces: []Iface{iface("a", 1, 0), iface("b", 1, 0)}, main: "a", finished: make(chan struct{})}
	var d *Download
	var mu sync.Mutex
	resolves := map[string]int{}
	env.resolve = func(id, ifc, page string) {
		mu.Lock()
		resolves[ifc]++
		mu.Unlock()
		go d.SetResolved(ifc, h.URL+"/good/"+ifc, "", page)
	}
	d = New(env, State{ID: "pasted", URL: h.URL + "/page", Dir: t.TempDir(), Size: -1})
	finish(t, env, d)
	if p, _ := h.count("/page"); p != 1 {
		t.Fatalf("%d probes of the pasted page, want 1", p)
	}
	mu.Lock()
	defer mu.Unlock()
	if resolves["a"] != 1 || resolves["b"] != 1 {
		t.Fatalf("resolves %v, want one per interface", resolves)
	}
	for _, i := range []string{"a", "b"} {
		if p, _ := h.count("/good/" + i); p != 1 {
			t.Fatalf("%d probes of %s's link, want 1", p, i)
		}
	}
}

// A link that probes fine but whose data is always refused, with no landing
// page known, gives up after maxHTTPErrors tries instead of looping.
func TestRequestsWhenDataIsAlwaysRefusedWithoutLandingPage(t *testing.T) {
	h := newCountingHoster(t, randomData(1<<20), func(string) bool { return true })
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 0)}, main: "eth"}
	env.resolve = func(id, ifc, page string) { t.Errorf("unexpected resolve") }
	d := New(env, State{ID: "loop", URL: h.URL + "/old", Dir: t.TempDir(), Size: -1})
	d.Start()
	t.Cleanup(func() { d.Stop(StatusPaused) })
	deadline := time.Now().Add(30 * time.Second)
	for d.Summary().Links["eth"].Status != LinkFailed {
		if time.Now().After(deadline) {
			t.Fatalf("link never gave up: %+v", d.Summary().Links["eth"])
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	p, g := h.count("/old")
	if p > maxHTTPErrors || g > maxHTTPErrors {
		t.Fatalf("%d probes and %d data GETs, want at most %d each", p, g, maxHTTPErrors)
	}
	if e := d.Summary().Links["eth"].Error; !strings.Contains(e, "gave up after") {
		t.Fatalf("error %q", e)
	}
}
