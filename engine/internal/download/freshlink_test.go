package download

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// cutWriter ends a response after n bytes, like a connection that drops.
type cutWriter struct {
	http.ResponseWriter
	n int
}

func (c *cutWriter) Write(p []byte) (int, error) {
	if c.n <= 0 {
		return 0, http.ErrHandlerTimeout
	}
	if len(p) > c.n {
		p = p[:c.n]
	}
	c.n -= len(p)
	return c.ResponseWriter.Write(p)
}

// freshLinkServer serves data at /old/big.bin until it expires (403), the
// same file at /fresh/big.bin, and another file of the same size and name at
// /other/big.bin. /old's responses
// drop after 1 MiB, so the download keeps coming back for more.
func freshLinkServer(data, other []byte, expired *atomic.Bool) *httptest.Server {
	mod := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/old/big.bin":
			if expired.Load() {
				http.Error(w, "link expired", http.StatusForbidden)
				return
			}
			http.ServeContent(&cutWriter{w, 1 << 20}, r, "big.bin", mod, bytes.NewReader(data))
		case "/fresh/big.bin":
			http.ServeContent(w, r, "big.bin", mod, bytes.NewReader(data))
		case "/other/big.bin":
			http.ServeContent(w, r, "big.bin", mod.Add(time.Hour), bytes.NewReader(other))
		}
	}))
}

// startExpiring runs a direct-link download (no landing page) until its link
// expires part way and it pauses, waiting for one. It returns the bytes it had.
func startExpiring(t *testing.T, srv *httptest.Server, expired *atomic.Bool, env *testEnv) (*Download, int64) {
	t.Helper()
	d := New(env, State{ID: "fresh", URL: srv.URL + "/old/big.bin", Dir: t.TempDir(), Size: -1})
	d.Start()
	t.Cleanup(func() { d.Stop(StatusPaused) }) // lets go of the .part file
	deadline := time.Now().Add(20 * time.Second)
	for d.Summary().Done < 3<<20 {
		if time.Now().After(deadline) {
			t.Fatalf("no progress: %+v", d.Summary())
		}
		time.Sleep(20 * time.Millisecond)
	}
	expired.Store(true)
	for {
		if d.Summary().Links["eth"].Status == LinkRetrying {
			d.RetryLink("eth") // don't wait out the pause after the dropped connection
		}
		s := d.Summary()
		if s.Status == StatusPaused && s.Links["eth"].Status == LinkNeedsPage {
			return d, s.Done
		}
		if time.Now().After(deadline) {
			t.Fatalf("didn't pause for a landing page: %+v", s)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A direct link that expires can be given a fresh link to the same file as
// its landing page: the download goes back in the queue and finishes with
// the bytes it had.
func TestFreshLinkResumesAnExpiredDirectDownload(t *testing.T) {
	data := randomData(12 << 20)
	var expired atomic.Bool
	srv := freshLinkServer(data, randomData(12<<20), &expired)
	defer srv.Close()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, time.Millisecond)}, main: "eth", finished: make(chan struct{})}
	var d *Download
	env.resolve = func(id, ifc, page string) {
		d.SetResolved(ifc, page, "", "") // the browser fetched the "page": it was the file
	}
	d, had := startExpiring(t, srv, &expired, env)

	d.SetPageURL(srv.URL + "/fresh/big.bin")
	if s := d.Summary(); s.Status != StatusQueued {
		t.Fatalf("a landing page for a stuck download queues it again, status %s", s.Status)
	}
	d.Start() // the queue's job
	select {
	case <-env.finished:
	case <-time.After(30 * time.Second):
		t.Fatalf("timeout: %+v", d.Summary())
	}
	if s := d.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s; links %+v", s.Status, s.Error, s.Links)
	}
	got, err := os.ReadFile(d.Path())
	if err != nil || !bytes.Equal(got, data) {
		i := 0
		for i < len(got) && i < len(data) && got[i] == data[i] {
			i++
		}
		t.Fatalf("finished file differs (err %v): len %d want %d, first difference at %d; %+v", err, len(got), len(data), i, d.Summary().Links)
	}
	if had <= 0 {
		t.Fatalf("had no bytes before the link expired")
	}
}

// A fresh link to another file is refused as a mismatch (the main
// connection's file changed, so the download fails), and the bytes the
// download had stay on disk.
func TestFreshLinkToAnotherFileIsRefused(t *testing.T) {
	data := randomData(12 << 20)
	var expired atomic.Bool
	srv := freshLinkServer(data, bytes.Repeat([]byte{7}, 12<<20), &expired)
	defer srv.Close()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, time.Millisecond)}, main: "eth", finished: make(chan struct{})}
	var d *Download
	env.resolve = func(id, ifc, page string) { d.SetResolved(ifc, page, "", "") }
	d, had := startExpiring(t, srv, &expired, env)

	d.SetPageURL(srv.URL + "/other/big.bin")
	d.Start()
	deadline := time.Now().Add(20 * time.Second)
	for {
		s := d.Summary()
		if s.Links["eth"].Status == LinkMismatch && (s.Status == StatusPaused || s.Status == StatusFailed) {
			if s.Done < had {
				t.Fatalf("bytes lost: had %d, now %d", had, s.Done)
			}
			if _, err := os.Stat(d.partPath()); err != nil {
				t.Fatalf("the .part file is gone: %v", err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("another file wasn't refused: %+v", s)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
