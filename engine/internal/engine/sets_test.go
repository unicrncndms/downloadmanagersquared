package engine

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/download"
)

// addParts adds a split archive's parts in the order given, none started.
func addParts(t *testing.T, e *Engine, names ...string) map[string]string {
	t.Helper()
	ids := map[string]string{}
	for _, n := range names {
		id, err := e.Add(AddRequest{URL: "https://example.com/" + n, Filename: n})
		if err != nil {
			t.Fatal(err)
		}
		ids[n] = id
	}
	return ids
}

// The parts of a split archive start in the archive's order, however they
// were added: part 1 is what extraction starts from.
func TestPartsOfASetQueueInPartOrder(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	addParts(t, e, "a.part3.rar", "other.bin", "a.part1.rar", "b.zip", "a.part2.rar", "b.z01")
	e.syncSets()
	var got []string
	for _, j := range e.queue.List() {
		got = append(got, j.Summary().URL[len("https://example.com/"):])
	}
	// Each set keeps the places its parts held; a split ZIP's .zip is its last part.
	want := []string{"a.part1.rar", "other.bin", "a.part2.rar", "b.z01", "a.part3.rar", "b.zip"}
	if !slices.Equal(got, want) {
		t.Fatalf("queue order %v, want %v", got, want)
	}
}

// Download as folder is one switch and one folder for the whole set: set on
// any part, every part takes it, and they stay one Set.
func TestSetFolderOnOnePartAppliesToItsWholeSet(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	ids := addParts(t, e, "a.part1.rar", "a.part2.rar", "a.part3.rar")
	e.syncSets()
	on, into := true, t.TempDir()
	if err := e.SetFolder(ids["a.part2.rar"], &on, &into); err != nil {
		t.Fatal(err)
	}
	e.syncSets()
	groups := map[string]bool{}
	for n, id := range ids {
		d, _ := e.get(id)
		f := d.State().Folder
		if f == nil || !f.On || f.Into != into {
			t.Fatalf("%s: folder %+v, want on into %s", n, f, into)
		}
		groups[d.Summary().Group] = true
	}
	if len(groups) != 1 || len(e.sets) != 1 {
		t.Fatalf("the parts are in %d groups and %d sets, want one of each", len(groups), len(e.sets))
	}
	off := false
	if err := e.SetFolder(ids["a.part1.rar"], &off, nil); err != nil {
		t.Fatal(err)
	}
	for n, id := range ids {
		d, _ := e.get(id)
		if f := d.State().Folder; f != nil && f.On {
			t.Fatalf("%s is still on after the set was switched off", n)
		}
	}
}

// With one download at a time, a set still extracts as it downloads, and its
// parts are fetched in the order that keeps the least on disk: front to
// back, after the end of the last part where that holds the list of files
// (a 7z, a ZIP; a RAR's volumes need nothing from their end).
func TestASetFetchesItsPartsInTheOrderThatKeepsTheLeastOnDisk(t *testing.T) {
	for _, c := range []struct {
		src, names []string
		want       []int // the parts, in the order first asked for; its last alone where the first two may go either way
	}{
		{[]string{"split-nonsolid.7z.001", "split-nonsolid.7z.002", "split-nonsolid.7z.003"}, []string{"big.7z.001", "big.7z.002", "big.7z.003"}, []int{0, 2, 1}},
		{[]string{"volume.part1.rar", "volume.part2.rar", "volume.part3.rar"}, []string{"big.part1.rar", "big.part2.rar", "big.part3.rar"}, []int{0, 1, 2}},
		{[]string{"splitwr.z01", "splitwr.z02", "splitwr.zip"}, []string{"big.z01", "big.z02", "big.zip"}, []int{1}},
		{[]string{"split7.zip.001", "split7.zip.002", "split7.zip.003"}, []string{"big.zip.001", "big.zip.002", "big.zip.003"}, []int{0, 2, 1}},
	} {
		names := c.names
		parts := map[string][]byte{}
		for i, n := range names {
			b, err := os.ReadFile(filepath.Join("..", "archive", "testdata", c.src[i]))
			if err != nil {
				t.Fatal(err)
			}
			parts[n] = b
		}
		var mu sync.Mutex
		var asked []string // each part, in the order first asked for
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := strings.TrimPrefix(r.URL.Path, "/")
			b, ok := parts[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			mu.Lock()
			if !slices.Contains(asked, name) {
				asked = append(asked, name)
			}
			mu.Unlock()
			w.Header().Set("ETag", `"`+name+`"`)
			http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
		}))

		e := loopbackEngine(t)
		into := t.TempDir()
		e.applySettings(func(s *Settings) { s.MaxParallel, s.FolderDefault, s.FolderDir = 3, true, into })
		var ds []*download.Download
		for _, n := range []string{names[1], names[2], names[0]} { // added in no order
			id, err := e.Add(AddRequest{URL: srv.URL + "/" + n})
			if err != nil {
				t.Fatal(err)
			}
			d, _ := e.get(id)
			ds = append(ds, d)
		}
		waitFor(t, names[0]+": the set extracted and its parts completed", 60*time.Second, func() bool {
			e.syncSets()
			e.schedule()
			f := ds[0].Summary().Folder
			return f != nil && f.Set != nil && f.Set.Finished &&
				!slices.ContainsFunc(ds, func(d *download.Download) bool { return d.Status() != download.StatusCompleted })
		})
		srv.Close()
		var want []string
		for _, i := range c.want {
			want = append(want, names[i])
		}
		if len(asked) != 3 || !slices.Equal(asked[3-len(want):], want) {
			t.Fatalf("the parts were asked for in the order %v, want %v", asked, want)
		}
		if got, _ := os.ReadDir(into); len(got) != 1 {
			t.Fatalf("%s: left in the folder: %v", names[0], got)
		}
	}
}

// serveNamed serves the archive package's test files src under names.
func serveNamed(t *testing.T, src, names []string) *httptest.Server {
	t.Helper()
	parts := map[string][]byte{}
	for i, n := range names {
		b, err := os.ReadFile(filepath.Join("..", "archive", "testdata", src[i]))
		if err != nil {
			t.Fatal(err)
		}
		parts[n] = b
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		b, ok := parts[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `"`+name+`"`)
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Download as folder switched on for a set of which a part is already
// downloaded as a file, into another folder than they were saved to: the
// parts stay one set, the file is extracted with the rest, and after a
// restart the set is still finished, not extracting.
func TestASetSwitchedToAFolderLateStaysOneSetAndStaysFinished(t *testing.T) {
	names := []string{"big.part1.rar", "big.part2.rar", "big.part3.rar"}
	srv := serveNamed(t, []string{"volume.part1.rar", "volume.part2.rar", "volume.part3.rar"}, names)
	dir := t.TempDir()
	e, err := open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	stop := e.Shutdown
	defer func() { stop() }()
	e.settings.SaveDir = t.TempDir()
	e.mu.Lock()
	e.refreshInterfacesLocked(loopbackIfaces())
	e.mu.Unlock()
	var ids []string
	for _, n := range names {
		id, err := e.Add(AddRequest{URL: srv.URL + "/" + n})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	get := func(e *Engine) []*download.Download {
		var ds []*download.Download
		for _, id := range ids {
			d, err := e.get(id)
			if err != nil {
				t.Fatal(err)
			}
			ds = append(ds, d)
		}
		return ds
	}
	ds := get(e)
	tick := func(e *Engine) { e.syncSets(); e.schedule() }
	// Part 1 downloads as a file while the others wait.
	e.queue.Pause(ids[1])
	e.queue.Pause(ids[2])
	waitFor(t, "part 1 as a file", 30*time.Second, func() bool {
		tick(e)
		return ds[0].Status() == download.StatusCompleted
	})
	if _, err := os.Stat(filepath.Join(e.settings.SaveDir, names[0])); err != nil {
		t.Fatal(err)
	}
	// The switch, then the folder, as the group card sends them: to a part not yet fetched.
	on, into := true, t.TempDir()
	if err := e.SetFolder(ids[1], &on, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.SetFolder(ids[1], nil, &into); err != nil {
		t.Fatal(err)
	}
	e.queue.Resume(ids[1])
	e.queue.Resume(ids[2])
	finished := func(ds []*download.Download) bool {
		groups := map[string]bool{}
		for _, d := range ds {
			s := d.Summary()
			groups[s.Group] = true
			if s.Status != download.StatusCompleted || s.Folder == nil || s.Folder.Set == nil || !s.Folder.Set.Finished {
				return false
			}
		}
		return len(groups) == 1
	}
	waitFor(t, "the set extracted as one", 60*time.Second, func() bool {
		tick(e)
		return finished(ds)
	})
	if got, _ := os.ReadDir(into); len(got) != 1 {
		t.Fatalf("left in the folder: %v", got)
	}
	if got, _ := os.ReadDir(e.settings.SaveDir); len(got) != 0 {
		t.Fatalf("left in the save folder: %v", got)
	}

	// An update restarts the engine: the set is as it was.
	e.Shutdown()
	e, err = open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	stop = e.Shutdown
	ds = get(e)
	waitFor(t, "the set still finished after a restart", 10*time.Second, func() bool {
		tick(e)
		return finished(ds)
	})
}

// A part added after its set was switched to a folder elsewhere lands in the
// save folder as a file: it takes the set's switch and folder, and the set
// extracts.
func TestAPartAddedAfterTheSwitchJoinsItsSet(t *testing.T) {
	names := []string{"big.part1.rar", "big.part2.rar", "big.part3.rar"}
	srv := serveNamed(t, []string{"volume.part1.rar", "volume.part2.rar", "volume.part3.rar"}, names)
	e := loopbackEngine(t)
	add := func(n string) *download.Download {
		id, err := e.Add(AddRequest{URL: srv.URL + "/" + n})
		if err != nil {
			t.Fatal(err)
		}
		d, _ := e.get(id)
		return d
	}
	ds := []*download.Download{add(names[1]), add(names[2])}
	e.syncSets()
	on, into := true, t.TempDir()
	if err := e.SetFolder(ds[0].ID(), &on, &into); err != nil {
		t.Fatal(err)
	}
	e.syncSets()
	ds = append(ds, add(names[0]))
	waitFor(t, "the set extracted as one", 60*time.Second, func() bool {
		e.syncSets()
		e.schedule()
		groups := map[string]bool{}
		for _, d := range ds {
			s := d.Summary()
			groups[s.Group] = true
			if s.Status != download.StatusCompleted || s.Folder == nil || s.Folder.Set == nil || !s.Folder.Set.Finished {
				return false
			}
		}
		return len(groups) == 1
	})
	if got, _ := os.ReadDir(e.settings.SaveDir); len(got) != 0 {
		t.Fatalf("left in the save folder: %v", got)
	}
}

// Parts of a set removed with their files while the set still waits for a
// part: their .part files go, also the fetched ones the set has open.
func TestRemovingASetsPartsWithFilesLeavesNoPart(t *testing.T) {
	names := []string{"big.part1.rar", "big.part2.rar"}
	srv := serveNamed(t, []string{"volume.part1.rar", "volume.part2.rar"}, names)
	e := loopbackEngine(t)
	var ds []*download.Download
	for _, n := range names {
		id, err := e.Add(AddRequest{URL: srv.URL + "/" + n})
		if err != nil {
			t.Fatal(err)
		}
		d, _ := e.get(id)
		ds = append(ds, d)
	}
	e.syncSets()
	on, into := true, t.TempDir()
	if err := e.SetFolder(ds[0].ID(), &on, &into); err != nil {
		t.Fatal(err)
	}
	parts := func() (n int) {
		for _, dir := range []string{into, e.settings.SaveDir} {
			got, _ := os.ReadDir(dir)
			for _, f := range got {
				if strings.HasSuffix(f.Name(), ".dms.part") {
					n++
				}
			}
		}
		return n
	}
	waitFor(t, "the set waiting for part 3", 60*time.Second, func() bool {
		e.syncSets()
		e.schedule()
		s := ds[1].Summary()
		return ds[0].Status() == download.StatusCompleted && s.Status == download.StatusCompleted &&
			s.Folder != nil && s.Folder.Set != nil && s.Folder.Set.Note != ""
	})
	if parts() == 0 {
		t.Fatal("no part kept for the set: nothing to check")
	}
	for _, d := range ds {
		if err := e.Remove(d.ID(), true); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "the parts' files gone", 10*time.Second, func() bool {
		e.syncSets()
		return parts() == 0
	})
}
