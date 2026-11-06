package download

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/archive"
	"downloadmanagersquared/engine/internal/winutil"
)

// lowerLarge makes Entries over n bytes large, freed every step bytes.
func lowerLarge(t *testing.T, n, step int64) {
	t.Helper()
	was, wasStep := archive.LargeEntry, archive.FreeStep
	archive.LargeEntry, archive.FreeStep = n, step
	t.Cleanup(func() { archive.LargeEntry, archive.FreeStep = was, wasStep })
}

// largeZip is a few small files around one large one.
func largeZip(t *testing.T, size int) ([]zipFile, []byte, archive.Entry) {
	t.Helper()
	files := manyFiles("", 3, 64<<10)
	files = append(files[:1], append([]zipFile{{name: "huge.bin", data: randomData(size)}}, files[1:]...)...)
	data := buildZip(t, files)
	a, err := archive.Read(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range a.Entries {
		if e.Name == "huge.bin" {
			return files, data, e
		}
	}
	t.Fatal("no huge.bin")
	return nil, nil, archive.Entry{}
}

func allocatedIn(f *os.File, start, end int64) int64 {
	rs, err := winutil.AllocatedRanges(f)
	if err != nil {
		return -1
	}
	var n int64
	for _, r := range rs {
		n += max(0, min(r.End, end)-max(r.Start, start))
	}
	return n
}

// countingServer counts the bytes it sends for data requests.
func countingServer(data []byte) (*zipServer, *atomic.Int64) {
	srv := newZipServer(data)
	var sent atomic.Int64
	inner := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rg := r.Header.Get("Range"); rg != "" && rg != "bytes=0-0" {
			start, end := parseRange(rg, int64(len(data)))
			sent.Add(end - start)
		}
		inner.ServeHTTP(w, r)
	})
	return srv, &sent
}

// A large Entry is extracted while it downloads, and its bytes are freed
// behind the decompressor: the .part never holds much of it.
func TestALargeEntryIsFreedWhileItIsExtracted(t *testing.T) {
	lowerLarge(t, 1<<20, 1<<20)
	files, data, e := largeZip(t, 32<<20)
	srv := newZipServer(data)
	defer srv.Close()
	var mu sync.Mutex
	worst := int64(0)
	afterExtract = func(f *os.File, _ int64) {
		mu.Lock()
		worst = max(worst, allocatedIn(f, e.Start, e.End))
		mu.Unlock()
	}
	t.Cleanup(func() { afterExtract = nil })
	into := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 2*time.Millisecond)}, main: "eth"}
	d := runToEnd(t, env, State{ID: "l1", URL: srv.URL + "/dl", Dir: into, Size: -1, Folder: &Folder{On: true, Into: into}})
	if s := d.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkTree(t, filepath.Join(into, "big"), files, "")
	mu.Lock()
	defer mu.Unlock()
	if worst == 0 || worst > (e.End-e.Start)/2 {
		t.Fatalf("up to %d of the large Entry's %d bytes allocated at once", worst, e.End-e.Start)
	}
}

// Stopped halfway through a large Entry, the download fetches again only the
// bytes of it that were freed, and ends correct.
func TestAStopHalfwayRefetchesOnlyTheFreedPart(t *testing.T) {
	lowerLarge(t, 1<<20, 1<<20)
	files, data, e := largeZip(t, 24<<20)
	srv, sent := countingServer(data)
	defer srv.Close()
	into := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 4*time.Millisecond)}, main: "eth", finished: make(chan struct{})}
	d := New(env, State{ID: "l2", URL: srv.URL + "/dl", Dir: into, Size: -1, Folder: &Folder{On: true, Into: into}})
	d.Start()
	deadline := time.Now().Add(30 * time.Second)
	for freedIn(d.State().Folder.Freed, e) < 4<<20 {
		if time.Now().After(deadline) {
			t.Fatalf("nothing of the large Entry freed: %+v", d.Summary())
		}
		time.Sleep(10 * time.Millisecond)
	}
	d.Stop(StatusPaused)
	st := d.State()
	if slices.Contains(st.Folder.Done, e.Start) {
		t.Fatal("the large Entry finished before it could be stopped")
	}
	freed := freedIn(st.Folder.Freed, e)
	var missing int64
	for _, r := range st.Remaining {
		missing += r.End - r.Start
	}
	out := st.Folder.Out
	// As after a crash: the unfinished file is still there.
	if err := os.WriteFile(filepath.Join(out, "huge.bin"+archive.PartSuffix), []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}

	sent.Store(0)
	st.Status = StatusQueued
	d2 := runToEnd(t, env, st)
	if s := d2.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkTree(t, out, files, "")
	if got, want := sent.Load(), missing+freed; got != want {
		t.Fatalf("fetched %d bytes after the stop, want %d (%d missing + %d freed of the large Entry)", got, want, missing, freed)
	}
	if l := leftoverParts(t, out); len(l) > 0 {
		t.Fatalf("left %v", l)
	}
}

func freedIn(freed []Range, e archive.Entry) int64 {
	var n int64
	for _, r := range freed {
		n += max(0, min(r.End, e.End)-max(r.Start, e.Start))
	}
	return n
}

func leftoverParts(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	filepath.WalkDir(dir, func(p string, _ os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, archive.PartSuffix) {
			found = append(found, p)
		}
		return nil
	})
	return found
}

// A wrong byte in a large Entry fails it whole: all of it is fetched again,
// freed bytes included, and it ends correct.
func TestACorruptedLargeEntryIsFetchedAgainWhole(t *testing.T) {
	lowerLarge(t, 1<<20, 1<<20)
	files, data, e := largeZip(t, 16<<20)
	srv, sent := countingServer(data)
	defer srv.Close()
	srv.corrupt = corruptOnce(data, e.Start+(e.End-e.Start)/2, 1)
	into := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 0)}, main: "eth"}
	d := runToEnd(t, env, State{ID: "l3", URL: srv.URL + "/dl", Dir: into, Size: -1, Folder: &Folder{On: true, Into: into}})
	if s := d.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkTree(t, filepath.Join(into, "big"), files, "")
	if got, want := sent.Load(), int64(len(data))+e.End-e.Start; got != want {
		t.Fatalf("fetched %d bytes, want %d (the archive and the large Entry again)", got, want)
	}
	if f := d.State().Folder; len(f.Failed) != 1 {
		t.Fatalf("failed %v, want the large Entry once", f.Failed)
	}
}
