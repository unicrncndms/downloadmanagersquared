package download

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A hoster whose fresh links pass the probe once and then refuse the data
// (one-time or IP-bound links): the engine asks for 3 new links, not forever.
func TestResolveCapHoldsWhenFreshLinksProbeFineButFailOnData(t *testing.T) {
	data := randomData(2 << 20)
	var mu sync.Mutex
	probed := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probe := r.Header.Get("Range") == "bytes=0-0"
		mu.Lock()
		first := !probed[r.URL.Path]
		if probe {
			probed[r.URL.Path] = true
		}
		mu.Unlock()
		if !strings.HasPrefix(r.URL.Path, "/fresh/") || !first || !probe {
			http.Error(w, "link used up", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="big.bin"`)
		http.ServeContent(w, r, "big.bin", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()

	var resolves atomic.Int32
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 0)}, main: "eth", finished: make(chan struct{})}
	var d *Download
	env.resolve = func(id, ifc, page string) {
		n := resolves.Add(1)
		go d.SetResolved(ifc, srv.URL+"/fresh/"+strconv.Itoa(int(n)), "", page)
	}
	d = New(env, State{ID: "cap", URL: srv.URL + "/old", PageURL: srv.URL + "/page", Dir: t.TempDir(), Size: -1})
	d.Start()
	t.Cleanup(func() { d.Stop(StatusPaused) })
	deadline := time.Now().Add(30 * time.Second)
	for d.Summary().Links["eth"].Status != LinkFailed {
		if time.Now().After(deadline) {
			t.Fatalf("link never gave up, %d resolves: %+v", resolves.Load(), d.Summary().Links["eth"])
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if n := resolves.Load(); n != maxResolves {
		t.Fatalf("%d resolves, want %d", n, maxResolves)
	}
	if e := d.Summary().Links["eth"].Error; !strings.Contains(e, "gave up after 3 new links") {
		t.Fatalf("error %q", e)
	}
}

// A link saved while it waited for the browser is asked for again after a restart.
func TestRestartedResolvingLinkGoesPending(t *testing.T) {
	d := New(&testEnv{ifaces: []Iface{iface("eth", 1, 0)}, main: "eth"},
		State{ID: "r", URL: "http://127.0.0.1/x", Dir: t.TempDir(),
			Links: map[string]*Link{"eth": {URL: "http://127.0.0.1/x", Status: LinkResolving, Resolves: 1}}})
	if s := d.st.Links["eth"].Status; s != LinkPending {
		t.Fatalf("status %s, want pending", s)
	}
}

// A data GET answered with an HTML page (200, the server ignores ranges) means
// the link died: it is resolved again and the page never reaches the file.
func TestHTMLAnswerToDataGetIsAnExpiredLink(t *testing.T) {
	data := randomData(2 << 20)
	var dead atomic.Bool // set once the old link has served its page
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probe := r.Header.Get("Range") == "bytes=0-0"
		if r.URL.Path == "/old" && (dead.Load() || !probe) {
			dead.Store(true)
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<html>link expired</html>")
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="big.bin"`)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		if !probe {
			w.Write(data)
		}
	}))
	defer srv.Close()

	var resolves atomic.Int32
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 0)}, main: "eth"}
	var d *Download
	env.resolve = func(id, ifc, page string) {
		resolves.Add(1)
		go d.SetResolved(ifc, srv.URL+"/good", "", page)
	}
	dir := t.TempDir()
	env.finished = make(chan struct{})
	d = New(env, State{ID: "html", URL: srv.URL + "/old", PageURL: srv.URL + "/page", Dir: dir, Size: -1})
	d.Start()
	select {
	case <-env.finished:
	case <-time.After(30 * time.Second):
		t.Fatalf("timeout: %+v", d.Summary())
	}
	if s := d.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s; %+v", s.Status, s.Error, s.Links)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
	if n := resolves.Load(); n != 1 {
		t.Fatalf("%d resolves, want 1", n)
	}
}
