package download

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/archive"
)

// roomFor has freeSpace say every drive has n bytes free, for this test.
func roomFor(t *testing.T, n int64) {
	t.Helper()
	was := freeSpace
	freeSpace = func(string) (int64, error) { return n, nil }
	t.Cleanup(func() { freeSpace = was })
}

// Each way of keeping the archive, through a whole download as folder: Free
// frees its bytes as before, Keep until extracted frees nothing and deletes
// the .part at the end, and Keep the archive leaves it whole under its name.
func TestEachArchiveKeepingThroughADownload(t *testing.T) {
	for _, k := range []string{KeepFree, KeepUntil, KeepAfter} {
		t.Run(k, func(t *testing.T) {
			files := manyFiles("", 6, 256<<10)
			data := buildZip(t, files)
			srv, sent := countingServer(data)
			defer srv.Close()
			into := t.TempDir()
			env := &testEnv{ifaces: []Iface{iface("eth", 1, 0)}, main: "eth"}
			d := runToEnd(t, env, State{ID: "k-" + k, URL: srv.URL + "/dl", Dir: into, Size: -1,
				Folder: &Folder{On: true, Into: into, Keep: k}})
			s := d.Summary()
			if s.Status != StatusCompleted {
				t.Fatalf("status %s: %s", s.Status, s.Error)
			}
			checkTree(t, filepath.Join(into, "big"), files, "")
			if got := sent.Load(); got != int64(len(data)) {
				t.Fatalf("fetched %d bytes, want the archive's %d once", got, len(data))
			}
			freed := d.State().Folder.Freed
			if (len(freed) > 0) != (k == KeepFree) {
				t.Fatalf("freed %v", freed)
			}
			if _, err := os.Stat(d.partPath()); !os.IsNotExist(err) {
				t.Fatalf("the .part is still there (%v)", err)
			}
			kept := filepath.Join(into, "big.zip")
			got, err := os.ReadFile(kept)
			switch {
			case k == KeepAfter && (err != nil || !bytes.Equal(got, data)):
				t.Fatalf("the archive kept is wrong (%v)", err)
			case k == KeepAfter && (d.State().Filename != "big.zip" || s.Note != "Extracted to big · archive kept"):
				t.Fatalf("named %q, note %q", d.State().Filename, s.Note)
			case k != KeepAfter && err == nil:
				t.Fatal("the archive was kept")
			}
		})
	}
}

// Automatic keeps the archive until it is extracted when both drives have
// room for it and what it extracts to, frees it when they don't, and frees it
// after all once free space falls below diskReserve while it keeps it.
func TestAutomaticArchiveKeepingDependsOnTheRoom(t *testing.T) {
	files := manyFiles("", 8, 256<<10)
	data := buildZip(t, files)
	listRead := func(t *testing.T) (*Extraction, []archivePart, []*fakePart, int64) {
		e, ps, fs := fakeExtraction(t, data, 1)
		fs[0].f.Keep = KeepAuto
		stepUntil(t, e, ps, "list read", func() bool { return e.arc != nil })
		return e, ps, fs, spaceNeeded(e.arc, nil)
	}
	all := []archive.Range{{Start: 0, End: int64(len(data))}}

	e, ps, fs, need := listRead(t)
	roomFor(t, need+int64(len(data))+diskReserve)
	if free, err := e.mayFree(ps, fs[0].partInfo(), all); err != nil || free || fs[0].f.Keeping != KeepUntil {
		t.Fatalf("with room: frees %v (%v), decided %q", free, err, fs[0].f.Keeping)
	}
	stepUntil(t, e, ps, "finished", func() bool { return e.finished })
	if len(fs[0].f.Freed) > 0 {
		t.Fatalf("kept, yet freed %v", fs[0].f.Freed)
	}
	// The disk fills up: it frees the archive, what is extracted at once.
	roomFor(t, diskReserve-1)
	e.roomAt = time.Time{} // it looks every 5 seconds
	if free, err := e.mayFree(ps, fs[0].partInfo(), nil); err != nil || !free || fs[0].f.Keeping != KeepFree {
		t.Fatalf("almost full: frees %v (%v), decided %q", free, err, fs[0].f.Keeping)
	}
	if len(fs[0].f.Freed) == 0 {
		t.Fatal("the Entries extracted while kept weren't freed")
	}

	e, ps, fs, need = listRead(t)
	roomFor(t, need+int64(len(data))+diskReserve-1)
	if free, err := e.mayFree(ps, fs[0].partInfo(), all); err != nil || !free || fs[0].f.Keeping != KeepFree {
		t.Fatalf("without room: frees %v (%v), decided %q", free, err, fs[0].f.Keeping)
	}
	stepUntil(t, e, ps, "finished", func() bool { return e.finished })
	if len(fs[0].f.Freed) == 0 {
		t.Fatal("decided to free, yet freed nothing")
	}
}

// Archive keeping takes only its values, and can't change once anything is
// freed: those bytes are gone.
func TestArchiveKeepingIsRefusedOnceLocked(t *testing.T) {
	d := New(&testEnv{}, State{ID: "k", URL: "https://x.example/a.zip", Filename: "a.zip", Folder: &Folder{On: true}})
	if err := d.SetArchiveKeeping("sometimes"); err == nil {
		t.Fatal("an unknown value was taken")
	}
	if err := d.SetArchiveKeeping(KeepAfter); err != nil || d.State().Folder.Keep != KeepAfter {
		t.Fatalf("refused before anything is freed: %v", err)
	}
	if f := d.Summary().Folder; f.Keep != KeepAfter || f.Keeping != KeepAfter || f.KeepFixed {
		t.Fatalf("summary %+v", f)
	}
	d.editFolder(func(f *Folder) bool {
		f.Keep, f.Freed = KeepAuto, []Range{{0, 64 << 10}}
		return true
	})
	if err := d.SetArchiveKeeping(KeepUntil); err == nil || d.State().Folder.Keep != KeepAuto {
		t.Fatalf("changed once freed (%v)", err)
	}
	if f := d.Summary().Folder; f.Keeping != KeepFree || !f.KeepFixed {
		t.Fatalf("summary once freed %+v", f)
	}
}

// Kept until extracted and stopped halfway through a large Entry, as by a
// crash, the download decodes it again from the bytes still on disk: it
// fetches only what was never fetched.
func TestAKeptArchiveStoppedHalfwayFetchesNothingAgain(t *testing.T) {
	lowerLarge(t, 1<<20, 1<<20)
	files, data, e := largeZip(t, 24<<20)
	srv, sent := countingServer(data)
	defer srv.Close()
	into := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 4*time.Millisecond)}, main: "eth", finished: make(chan struct{})}
	d := New(env, State{ID: "k2", URL: srv.URL + "/dl", Dir: into, Size: -1, Folder: &Folder{On: true, Into: into, Keep: KeepUntil}})
	d.Start()
	waitUntil(t, "some of the large Entry extracted", func() bool {
		f := d.Summary().Folder
		return f != nil && f.Extracted >= e.Start+4<<20
	})
	if c := d.RestartCost(); c != 0 {
		t.Fatalf("a restart would cost %d bytes, want none", c)
	}
	d.Stop(StatusPaused)
	st := d.State()
	if slices.Contains(st.Folder.Done, e.Start) {
		t.Fatal("the large Entry finished before it could be stopped")
	}
	if len(st.Folder.Freed) > 0 {
		t.Fatalf("kept, yet freed %v", st.Folder.Freed)
	}
	var missing int64
	for _, r := range st.Remaining {
		missing += r.End - r.Start
	}
	sent.Store(0)
	d.dropParked() // as after a crash: the decompressor is gone
	if err := os.WriteFile(filepath.Join(st.Folder.Out, "huge.bin"+archive.PartSuffix), []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}

	st.Status = StatusQueued
	d2 := runToEnd(t, env, st)
	if s := d2.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkTree(t, st.Folder.Out, files, "")
	if got := sent.Load(); got != missing {
		t.Fatalf("fetched %d bytes after the stop, want only the %d missing", got, missing)
	}
	if _, err := os.Stat(d2.partPath()); !os.IsNotExist(err) {
		t.Fatalf("the .part is still there (%v)", err)
	}
}

// A RAR in volumes kept until extracted, stopped while its decoder is partway
// and started again, decodes from its front again without fetching any of its
// first volume again, and deletes its parts at the end.
func TestAKeptRarSetStoppedPartwayFetchesNothingAgain(t *testing.T) {
	was, wasStep := archive.LargeEntry, archive.FreeStep
	archive.LargeEntry, archive.FreeStep = 1<<10, 4<<10
	t.Cleanup(func() { archive.LargeEntry, archive.FreeStep = was, wasStep })

	seven, err := os.ReadFile(filepath.Join("..", "archive", "testdata", "solid.7z"))
	if err != nil {
		t.Fatal(err)
	}
	var src, served []string
	var later int64 // the bytes of volumes 2 and 3, fetched only after the stop
	for i := 1; i <= 3; i++ {
		f := fmt.Sprintf("volume.part%d.rar", i)
		src, served = append(src, f), append(served, fmt.Sprintf("big.part%d.rar", i))
		if fi, err := os.Stat(filepath.Join("..", "archive", "testdata", f)); err == nil && i > 1 {
			later += fi.Size()
		}
	}
	_, srv := serveFiles(t, src, served)
	var sent atomic.Int64
	inner := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rg := r.Header.Get("Range"); rg != "bytes=0-0" {
			var n int
			fmt.Sscanf(r.URL.Path, "/%d", &n)
			if fi, err := os.Stat(filepath.Join("..", "archive", "testdata", fmt.Sprintf("volume.part%d.rar", n))); err == nil {
				start, end := int64(0), fi.Size()
				if rg != "" {
					start, end = parseRange(rg, fi.Size())
				}
				sent.Add(end - start)
			}
		}
		inner.ServeHTTP(w, r)
	})
	into := t.TempDir()
	env := &setEnv{testEnv: testEnv{ifaces: []Iface{iface("eth", 2, 0)}, main: "eth", keeping: KeepUntil}}
	p := map[int]*Download{1: part(env, srv, 1, into), 2: part(env, srv, 2, into), 3: part(env, srv, 3, into)}
	p[1].Start()
	waitUntil(t, "part 1 completed", completed(p[1]))
	s := NewSet("k", "big.rar")
	s.SetMembers(p)
	s.Start()
	waitUntil(t, "the decoder partway into part 1", func() bool {
		v := s.Summary()
		return v != nil && v.Extracted > 0
	})
	if c := s.RestartCost(); c != 0 {
		t.Fatalf("a restart would cost %d bytes, want none", c)
	}
	s.Stop()
	s.SetMembers(nil)
	if v := s.Summary(); v != nil && v.Finished {
		t.Skip("extracted before it could be stopped")
	}
	if f := p[1].State().Folder; len(f.Freed) > 0 {
		t.Fatalf("kept, yet freed %v", f.Freed)
	}
	sent.Store(0)

	s = NewSet("k", "big.rar")
	s.SetMembers(p)
	s.Start()
	defer s.Stop()
	waitUntil(t, "set finished", func() bool {
		for _, d := range p {
			if st := d.Summary().Status; st == "" || st == StatusQueued || st == StatusPaused {
				d.Start()
			}
		}
		v := s.Summary()
		return v != nil && v.Finished
	})
	checkSet(t, seven, filepath.Join(into, "big", "Game"))
	if got := sent.Load(); got != later {
		t.Fatalf("fetched %d bytes after the stop, want only volumes 2 and 3's %d", got, later)
	}
	waitUntil(t, "the parts deleted", func() bool {
		left, _ := filepath.Glob(filepath.Join(into, "*"+partSuffix))
		return len(left) == 0
	})
	if _, err := os.Stat(filepath.Join(into, "big.part1.rar")); err == nil {
		t.Fatal("a part was kept")
	}
}

// Kept afterwards, a split archive's parts stay under their own names.
func TestAKeptSetKeepsItsPartsNamed(t *testing.T) {
	whole, srv := serveParts(t, "split-nonsolid.7z")
	into := t.TempDir()
	env := &setEnv{testEnv: testEnv{ifaces: []Iface{iface("eth", 2, 0)}, main: "eth", keeping: KeepAfter}}
	p := map[int]*Download{}
	for n := 1; ; n++ {
		if _, err := os.Stat(filepath.Join("..", "archive", "testdata", fmt.Sprintf("split-nonsolid.7z.%03d", n))); err != nil {
			break
		}
		p[n] = part(env, srv, n, into)
	}
	s := NewSet("k", "big.7z")
	s.SetMembers(p)
	s.Start()
	defer s.Stop()
	for _, d := range p {
		d.Start()
	}
	waitUntil(t, "set finished", func() bool { v := s.Summary(); return v != nil && v.Finished })
	for _, d := range p {
		waitUntil(t, "parts completed", completed(d))
	}
	waitUntil(t, "parts named", func() bool {
		left, _ := filepath.Glob(filepath.Join(into, "*"+partSuffix))
		return len(left) == 0
	})
	checkSet(t, whole, filepath.Join(into, "Game"))
	var kept []byte
	for n := 1; n <= len(p); n++ {
		b, err := os.ReadFile(filepath.Join(into, fmt.Sprintf("big.7z.%03d", n)))
		if err != nil {
			t.Fatalf("part %d not kept: %v", n, err)
		}
		kept = append(kept, b...)
	}
	if !bytes.Equal(kept, whole) {
		t.Fatal("the parts kept aren't the archive")
	}
}
