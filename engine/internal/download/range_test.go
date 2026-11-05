package download

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// version is one state of the file on a server: its bytes and its validators.
type version struct {
	data []byte
	etag string
	mod  time.Time
}

// changingServer serves whichever version is current. Replacing it drops the
// responses under way, as a server restart does, so the engine has to ask again
// and meets the new version. (Responses that kept going with the old version
// would just finish the old file, which is fine.)
type changingServer struct {
	*httptest.Server
	mu      sync.Mutex
	gen     int
	cur     version
	perLink map[string]version // what a given interface gets instead, like a CDN node out of step
	ignore  map[string]bool    // interfaces whose data requests get the whole file (200), Range ignored
	served  atomic.Int64       // data bytes sent, probes excluded
}

func newChangingServer(v version) *changingServer {
	s := &changingServer{cur: v, perLink: map[string]version{}, ignore: map[string]bool{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

func (s *changingServer) set(v version) {
	s.mu.Lock()
	s.cur = v
	s.gen++
	s.mu.Unlock()
}

// cutOnChange drops its response once the server's version has moved on.
type cutOnChange struct {
	http.ResponseWriter
	s   *changingServer
	gen int
}

func (c cutOnChange) Write(p []byte) (int, error) {
	c.s.mu.Lock()
	moved := c.s.gen != c.gen
	c.s.mu.Unlock()
	if moved {
		panic(http.ErrAbortHandler)
	}
	return c.ResponseWriter.Write(p)
}

func (s *changingServer) serve(w http.ResponseWriter, r *http.Request) {
	tag := r.Header.Get("X-Iface")
	probe := r.Header.Get("Range") == "bytes=0-0"
	s.mu.Lock()
	v, ok := s.perLink[tag]
	if !ok {
		v = s.cur
	}
	ignore := s.ignore[tag] && !probe
	gen := s.gen
	s.mu.Unlock()
	w = cutOnChange{w, s, gen}
	w.Header().Set("Content-Disposition", `attachment; filename="big.bin"`)
	if v.etag != "" {
		w.Header().Set("ETag", v.etag)
	}
	if !probe {
		w = countingWriter{w, &s.served}
	}
	if ignore {
		r.Header.Del("Range")
	}
	http.ServeContent(w, r, "big.bin", v.mod, bytes.NewReader(v.data))
}

var (
	t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	t1 = t0.Add(time.Hour)
)

// A connection whose data requests come back as the whole file (200) instead of
// the part asked for is dropped; the file still comes out right.
func TestServerIgnoringRangeOnOneConnectionNeverCorrupts(t *testing.T) {
	data := randomData(16 << 20)
	srv := newChangingServer(version{data, `"v1"`, t0})
	defer srv.Close()
	srv.ignore["wifi"] = true
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 2*time.Millisecond), iface("wifi", 2, 0)}, main: "eth"}
	d := runToEnd(t, env, State{ID: "r1", URL: srv.URL + "/big.bin", Dir: dir, Size: -1})
	s := d.Summary()
	if s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	if l := s.Links["wifi"]; l.Status != LinkFailed {
		t.Errorf("wifi should be dropped for ignoring ranges, got %+v", l)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
}

// waitServed blocks until the server has sent n data bytes.
func waitServed(t *testing.T, srv *changingServer, n int64) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for srv.served.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("server sent only %d bytes", srv.served.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// failedBecauseChanged: the download stopped and says the file changed, and no
// finished file was made from the pieces.
func failedBecauseChanged(t *testing.T, d *Download, dir string) {
	t.Helper()
	s := d.Summary()
	if s.Status != StatusFailed || !strings.Contains(s.Error, "changed") {
		t.Fatalf("want failed because the file changed, got status %s: %s", s.Status, s.Error)
	}
	if _, err := os.Stat(filepath.Join(dir, "big.bin")); err == nil {
		t.Errorf("a finished file was made from two versions")
	}
}

// The file is replaced (same size) while it downloads: what arrives after the
// change must not be stitched onto what came before.
func TestFileReplacedMidDownloadIsNotStitched(t *testing.T) {
	v1 := randomData(24 << 20)
	v2 := bytes.Repeat([]byte{7}, len(v1))
	srv := newChangingServer(version{v1, `"v1"`, t0})
	defer srv.Close()
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 4*time.Millisecond), iface("wifi", 1, 6*time.Millisecond)}, main: "eth"}
	env.finished = make(chan struct{})
	d := New(env, State{ID: "r2", URL: srv.URL + "/big.bin", Dir: dir, Size: -1})
	d.Start()
	waitServed(t, srv, 4<<20)
	srv.set(version{v2, `"v2"`, t1})
	select {
	case <-env.finished:
	case <-time.After(60 * time.Second):
		t.Fatalf("download did not stop: %+v", d.Summary())
	}
	failedBecauseChanged(t, d, dir)
}

// Only Last-Modified to go on, no ETag: still caught.
func TestFileReplacedMidDownloadIsCaughtByLastModified(t *testing.T) {
	v1 := randomData(24 << 20)
	v2 := bytes.Repeat([]byte{7}, len(v1))
	srv := newChangingServer(version{v1, "", t0})
	defer srv.Close()
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 4*time.Millisecond)}, main: "eth"}
	env.finished = make(chan struct{})
	d := New(env, State{ID: "r3", URL: srv.URL + "/big.bin", Dir: dir, Size: -1})
	d.Start()
	waitServed(t, srv, 4<<20)
	srv.set(version{v2, "", t1})
	select {
	case <-env.finished:
	case <-time.After(60 * time.Second):
		t.Fatalf("download did not stop: %+v", d.Summary())
	}
	failedBecauseChanged(t, d, dir)
}

// The file shrinks mid-download: chunks past its new end get 416, chunks inside
// it a different total size. Either way it changed.
func TestFileShrinkingMidDownloadIsNotStitched(t *testing.T) {
	v1 := randomData(24 << 20)
	srv := newChangingServer(version{v1, `"v1"`, t0})
	defer srv.Close()
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 4*time.Millisecond)}, main: "eth"}
	env.finished = make(chan struct{})
	d := New(env, State{ID: "r4", URL: srv.URL + "/big.bin", Dir: dir, Size: -1})
	d.Start()
	waitServed(t, srv, 4<<20)
	srv.set(version{v1[:2<<20], `"v1"`, t0}) // same validators: only the size gives it away
	select {
	case <-env.finished:
	case <-time.After(60 * time.Second):
		t.Fatalf("download did not stop: %+v", d.Summary())
	}
	failedBecauseChanged(t, d, dir)
}

// A 416 from a server whose file did not visibly change is still not data.
func TestRangeNotSatisfiableIsNeverTakenAsData(t *testing.T) {
	data := randomData(16 << 20)
	var refuse atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refuse.Load() && r.Header.Get("Range") != "bytes=0-0" {
			w.Header().Set("Content-Range", "bytes */16777216")
			http.Error(w, "nope", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		http.ServeContent(w, r, "big.bin", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	refuse.Store(true)
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 0)}, main: "eth"}
	d := runToEnd(t, env, State{ID: "r5", URL: srv.URL + "/big.bin", Dir: dir, Size: -1})
	if s := d.Summary(); s.Status == StatusCompleted {
		t.Fatalf("completed from 416 answers")
	}
	if _, err := os.Stat(filepath.Join(dir, "big.bin")); err == nil {
		t.Errorf("a finished file was made")
	}
}

// The file is replaced while the download is paused (or the engine is off):
// resuming must not add the new version's bytes to the old one's.
func TestFileReplacedWhilePausedIsNotResumed(t *testing.T) {
	v1 := randomData(24 << 20)
	v2 := bytes.Repeat([]byte{7}, len(v1))
	srv := newChangingServer(version{v1, `"v1"`, t0})
	defer srv.Close()
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 3, 3*time.Millisecond)}, main: "eth"}
	d := New(env, State{ID: "r6", URL: srv.URL + "/big.bin", Dir: dir, Size: -1})
	d.Start()
	for d.Summary().Done < 4<<20 {
		time.Sleep(20 * time.Millisecond)
	}
	d.Stop(StatusPaused)
	st := d.State()
	if len(st.Remaining) == 0 {
		t.Fatalf("finished before the pause")
	}

	srv.set(version{v2, `"v2"`, t1})
	st.Status = StatusQueued
	d2 := runToEnd(t, env, st) // a fresh Download from the saved state, as after a restart
	failedBecauseChanged(t, d2, dir)
}

// The same file across a pause, unchanged, resumes as before.
func TestUnchangedFileResumesAcrossARestart(t *testing.T) {
	data := randomData(24 << 20)
	srv := newChangingServer(version{data, `"v1"`, t0})
	defer srv.Close()
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 3, 3*time.Millisecond)}, main: "eth"}
	d := New(env, State{ID: "r7", URL: srv.URL + "/big.bin", Dir: dir, Size: -1})
	d.Start()
	for d.Summary().Done < 4<<20 {
		time.Sleep(20 * time.Millisecond)
	}
	d.Stop(StatusPaused)
	st := d.State()
	st.Status = StatusQueued
	env.ifaces = []Iface{iface("eth", 3, 0)}
	d2 := runToEnd(t, env, st)
	if s := d2.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
}

// Another connection gets a different version of the file (same size and name):
// it sits out, and the download finishes from the version it started with.
func TestConnectionServingAnotherVersionIsExcluded(t *testing.T) {
	data := randomData(16 << 20)
	srv := newChangingServer(version{data, `"v1"`, t0})
	defer srv.Close()
	srv.perLink["wifi"] = version{bytes.Repeat([]byte{7}, len(data)), `"v2"`, t1}
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 0), iface("wifi", 2, 0)}, main: "eth"}
	d := runToEnd(t, env, State{ID: "r8", URL: srv.URL + "/big.bin", Dir: dir, Size: -1})
	s := d.Summary()
	if s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	if l := s.Links["wifi"]; l.Status != LinkMismatch {
		t.Errorf("wifi serves another version and should sit out, got %+v", l)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
}

// CDN nodes in step but with their own ETags (Apache's inode-based ones): the
// same Last-Modified means the same file, so both connections are used.
func TestSameLastModifiedWithOtherETagIsTheSameFile(t *testing.T) {
	data := randomData(16 << 20)
	srv := newChangingServer(version{data, `"node1"`, t0})
	defer srv.Close()
	srv.perLink["wifi"] = version{data, `"node2"`, t0}
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 2*time.Millisecond), iface("wifi", 2, 2*time.Millisecond)}, main: "eth"}
	d := runToEnd(t, env, State{ID: "r9", URL: srv.URL + "/big.bin", Dir: dir, Size: -1})
	s := d.Summary()
	if s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	if l := s.Links["wifi"]; l.Status == LinkMismatch {
		t.Errorf("wifi has the same file and should not sit out: %+v", l)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
}

func TestSameVersion(t *testing.T) {
	cases := []struct {
		name string
		a, b Version
		same bool
	}{
		{"nothing known", Version{}, Version{}, true},
		{"one side unknown", Version{ETag: `"a"`}, Version{}, true},
		{"same etag", Version{ETag: `"a"`}, Version{ETag: `"a"`}, true},
		{"other etag", Version{ETag: `"a"`}, Version{ETag: `"b"`}, false},
		{"weak etags say nothing", Version{ETag: `W/"a"`}, Version{ETag: `W/"b"`}, true},
		{"same date", Version{LastModified: "Fri, 02 Jan 2026 03:04:05 GMT"}, Version{LastModified: "Fri, 02 Jan 2026 03:04:05 GMT"}, true},
		{"other date", Version{LastModified: "Fri, 02 Jan 2026 03:04:05 GMT"}, Version{LastModified: "Fri, 02 Jan 2026 04:04:05 GMT"}, false},
		{"date decides over etag", Version{ETag: `"a"`, LastModified: "x"}, Version{ETag: `"b"`, LastModified: "x"}, true},
		{"etag when a date is missing", Version{ETag: `"a"`, LastModified: "x"}, Version{ETag: `"b"`}, false},
	}
	for _, c := range cases {
		if got := c.a.same(c.b); got != c.same {
			t.Errorf("%s: same = %v, want %v", c.name, got, c.same)
		}
	}
}
