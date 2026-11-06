package archive

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/winutil"
)

// download is an archive arriving in a sparse file, as a download writes it.
type download struct {
	t       *testing.T
	data    []byte
	f       *os.File
	missing []Range
}

func newDownload(t *testing.T, data []byte) *download {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "big.zip.dms.part"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	if err := winutil.SetSparse(f, true); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(int64(len(data))); err != nil {
		t.Fatal(err)
	}
	return &download{t: t, data: data, f: f, missing: []Range{{0, int64(len(data))}}}
}

// arrive writes the bytes [start, end) and takes them off missing.
func (d *download) arrive(start, end int64) {
	d.t.Helper()
	if _, err := d.f.WriteAt(d.data[start:end], start); err != nil {
		d.t.Fatal(err)
	}
	var left []Range
	for _, r := range d.missing {
		if r.End <= start || r.Start >= end {
			left = append(left, r)
			continue
		}
		if r.Start < start {
			left = append(left, Range{r.Start, start})
		}
		if r.End > end {
			left = append(left, Range{end, r.End})
		}
	}
	d.missing = left
}

// tail writes the central directory and reads it, as the download does first.
func (d *download) tail() *Archive {
	d.t.Helper()
	size := int64(len(d.data))
	need, err := Need(bytes.NewReader(d.data), size, TailGuess)
	if err != nil {
		d.t.Fatal(err)
	}
	d.arrive(size-min(need, size), size)
	a, err := Read(d.f, size)
	if err != nil {
		d.t.Fatal(err)
	}
	return a
}

func (d *download) allocatedIn(r Range) int64 {
	d.t.Helper()
	rs, err := winutil.AllocatedRanges(d.f)
	if err != nil {
		d.t.Fatal(err)
	}
	var n int64
	for _, a := range rs {
		n += max(0, min(a.End, r.End)-max(a.Start, r.Start))
	}
	return n
}

func extractor(t *testing.T, a *Archive, f *os.File, dir string, done ...int64) *Extractor {
	t.Helper()
	x, err := NewExtractor(a, f, dir, done)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func extract(t *testing.T, x *Extractor, missing []Range) Result {
	t.Helper()
	r, err := x.Extract(context.Background(), missing)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return r
}

// leftovers lists the unfinished files under dir.
func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	filepath.WalkDir(dir, func(p string, _ os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".dms.part") {
			found = append(found, p)
		}
		return nil
	})
	return found
}

func covered(missing []Range, e Entry) bool {
	for _, r := range missing {
		if r.Start < e.End && r.End > e.Start {
			return false
		}
	}
	return true
}

func TestEntriesAreExtractedAndFreedAsTheirBytesArriveInAnyOrder(t *testing.T) {
	files := []goFile{
		{name: "big/readme.txt", data: []byte(strings.Repeat("hello ", 2000))},
		{name: "big/data/one.bin", data: random(300 << 10), store: true},
		{name: "big/data/two.bin", data: random(500 << 10)},
		{name: "big/empty/"},
		{name: "big/data/zero.txt"},
		{name: "big/data/three.bin", data: random(700 << 10), store: true},
		{name: "big/text.txt", data: bytes.Repeat([]byte("abcdefgh"), 100<<10)},
	}
	for i := range 20 {
		files = append(files, goFile{name: "big/small/" + string(rune('a'+i)) + ".txt", data: random(1000 + i*97)})
	}
	data := goZip(t, "", files...)
	d := newDownload(t, data)
	a := d.tail()
	out := filepath.Join(t.TempDir(), "big")
	x := extractor(t, a, d.f, out)

	// The rest arrives in pieces, out of order.
	var pieces []Range
	for off := int64(0); off < a.DirStart; off += 97 << 10 {
		pieces = append(pieces, Range{off, min(off+97<<10, a.DirStart)})
	}
	rand.New(rand.NewSource(1)).Shuffle(len(pieces), func(i, j int) { pieces[i], pieces[j] = pieces[j], pieces[i] })

	done := map[int64]bool{}
	var freed []Range
	for _, p := range pieces {
		d.arrive(p.Start, p.End)
		r := extract(t, x, d.missing)
		if len(r.Failed) > 0 {
			t.Fatalf("failed: %v", r.Failed[0])
		}
		for _, e := range r.Done {
			if done[e.Start] {
				t.Fatalf("%s reported done twice", e.Name)
			}
			done[e.Start] = true
		}
		freed = append(freed, r.Freed...)

		for _, e := range a.Entries {
			if covered(d.missing, e) != done[e.Start] {
				t.Fatalf("%s: all its bytes there %v, done %v", e.Name, covered(d.missing, e), done[e.Start])
			}
			if !done[e.Start] {
				continue
			}
			// Its space is back, apart from allocation units it shares
			// with bytes still needed.
			if n := d.allocatedIn(Range{e.Start, e.End}); n > 128<<10 {
				t.Fatalf("%s: %d of its bytes still allocated", e.Name, n)
			}
		}
		if l := leftovers(t, out); len(l) > 0 {
			t.Fatalf("unfinished files left: %v", l)
		}
	}

	if len(done) != len(a.Entries) {
		t.Fatalf("%d of %d Entries done", len(done), len(a.Entries))
	}
	if n, of := x.Progress(); n != 26 || of != 26 {
		t.Fatalf("progress %d of %d, want 26 of 26", n, of)
	}
	for _, f := range files {
		p := filepath.Join(out, filepath.FromSlash(strings.TrimPrefix(f.name, "big/")))
		if strings.HasSuffix(f.name, "/") {
			if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
				t.Fatalf("folder %s: %v", f.name, err)
			}
			continue
		}
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, f.data) {
			t.Fatalf("%s: wrong content", f.name)
		}
	}
	// Nothing of the Entries is left; only the tail is still allocated.
	if n := d.allocatedIn(Range{0, a.DirStart}); n > 64<<10 {
		t.Fatalf("%d bytes before the central directory still allocated", n)
	}
	var sum int64
	for _, r := range freed {
		sum += r.End - r.Start
	}
	if sum != a.DirStart {
		t.Fatalf("freed %d bytes, the Entries take %d", sum, a.DirStart)
	}
}

func TestAWrongByteFailsTheEntryAndLeavesNoFile(t *testing.T) {
	data := goZip(t, "",
		goFile{name: "a.bin", data: random(200 << 10)},
		goFile{name: "b.bin", data: random(100 << 10), store: true},
	)
	for _, name := range []string{"a.bin", "b.bin"} {
		t.Run(name, func(t *testing.T) {
			bad := slices.Clone(data)
			d := newDownload(t, bad)
			a := d.tail()
			var e Entry
			for _, x := range a.Entries {
				if x.Name == name {
					e = x
				}
			}
			bad[e.End-200] ^= 0x55 // in the data, before any descriptor
			d.arrive(0, a.DirStart)

			out := t.TempDir()
			x := extractor(t, a, d.f, out)
			r := extract(t, x, d.missing)
			if len(r.Failed) != 1 || r.Failed[0].Entry.Name != name || r.Failed[0].Range != (Range{e.Start, e.End}) {
				t.Fatalf("failed %v, want %s %d-%d", r.Failed, name, e.Start, e.End)
			}
			if len(r.Done) != 1 || r.Done[0].Name == name {
				t.Fatalf("done %v, want only the other Entry", r.Done)
			}
			if _, err := os.Stat(filepath.Join(out, name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s exists after failing: %v", name, err)
			}
			if l := leftovers(t, out); len(l) > 0 {
				t.Fatalf("left %v", l)
			}
			if d.allocatedIn(Range{e.Start, e.End}) < e.End-e.Start-128<<10 {
				t.Fatalf("a failed Entry's bytes were freed")
			}

			// Fetched again, it comes out right.
			copy(bad, data)
			d.arrive(e.Start, e.End)
			r = extract(t, x, d.missing)
			if len(r.Failed) != 0 || len(r.Done) != 1 || r.Done[0].Name != name {
				t.Fatalf("after fetching again: done %v, failed %v", r.Done, r.Failed)
			}
		})
	}
}

func TestASizeThatDoesntMatchFailsTheEntry(t *testing.T) {
	data := goZip(t, "", goFile{name: "a.txt", data: []byte(strings.Repeat("x", 5000))})
	d := newDownload(t, data)
	a := d.tail()
	d.arrive(0, a.DirStart)
	for _, size := range []int64{4999, 5001} {
		a.Entries[0].Size = size
		out := t.TempDir()
		r := extract(t, extractor(t, a, d.f, out), d.missing)
		if len(r.Failed) != 1 || len(r.Done) != 0 {
			t.Fatalf("size %d: done %v, failed %v", size, r.Done, r.Failed)
		}
		if _, err := os.Stat(filepath.Join(out, "a.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("size %d: the file was kept", size)
		}
	}
}

func TestTimesReadOnlyAndEmptyFoldersAreRestored(t *testing.T) {
	when := time.Date(2019, 3, 4, 5, 6, 8, 0, time.UTC)
	dirWhen := time.Date(2020, 1, 2, 3, 4, 6, 0, time.UTC)
	data := goZip(t, "",
		goFile{name: "top/", modified: dirWhen, mode: 0o755 | os.ModeDir},
		goFile{name: "top/old.txt", data: []byte("old"), modified: when},
		goFile{name: "top/locked.txt", data: []byte("locked"), mode: 0o444, modified: when},
		goFile{name: "top/nothing/", modified: dirWhen},
		goFile{name: "other.txt", data: []byte("other"), modified: when},
	)
	d := newDownload(t, data)
	a := d.tail()
	d.arrive(0, a.DirStart)
	out := t.TempDir()
	r := extract(t, extractor(t, a, d.f, out), d.missing)
	if len(r.Done) != len(a.Entries) {
		t.Fatalf("%d of %d done", len(r.Done), len(a.Entries))
	}

	for _, name := range []string{"top/old.txt", "top/locked.txt", "other.txt"} {
		fi, err := os.Stat(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		if !fi.ModTime().Equal(when) {
			t.Fatalf("%s modified %v, want %v", name, fi.ModTime().UTC(), when)
		}
		if ro := fi.Mode().Perm()&0o200 == 0; ro != (name == "top/locked.txt") {
			t.Fatalf("%s read-only %v", name, ro)
		}
	}
	for _, name := range []string{"top", "top/nothing"} {
		fi, err := os.Stat(filepath.Join(out, name))
		if err != nil || !fi.IsDir() {
			t.Fatalf("%s: %v", name, err)
		}
		// A folder keeps its time though files were written into it after.
		if !fi.ModTime().Equal(dirWhen) {
			t.Fatalf("%s modified %v, want %v", name, fi.ModTime().UTC(), dirWhen)
		}
	}
}

func TestAFolderKeepsItsTimeWhenItsFilesComeLater(t *testing.T) {
	dirWhen := time.Date(2020, 1, 2, 3, 4, 6, 0, time.UTC)
	data := goZip(t, "",
		goFile{name: "top/", modified: dirWhen},
		goFile{name: "top/a.bin", data: random(200 << 10), store: true},
	)
	d := newDownload(t, data)
	a := d.tail()
	out := t.TempDir()
	x := extractor(t, a, d.f, out)
	d.arrive(a.Entries[0].Start, a.Entries[0].End)
	extract(t, x, d.missing)
	d.arrive(a.Entries[1].Start, a.Entries[1].End)
	if r := extract(t, x, d.missing); len(r.Done) != 1 {
		t.Fatalf("done %v", r.Done)
	}
	// "top" is the Root, so the output folder stands for it.
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(dirWhen) {
		t.Fatalf("the output folder modified %v, want %v", fi.ModTime().UTC(), dirWhen)
	}
}

func TestTheRootFolderIsTheOutputFolder(t *testing.T) {
	data := goZip(t, "",
		goFile{name: "Game/"},
		goFile{name: "Game/bin/game.exe", data: []byte("MZ")},
		goFile{name: "Game/readme.txt", data: []byte("hi")},
	)
	d := newDownload(t, data)
	a := d.tail()
	if a.Root != "Game" {
		t.Fatalf("root %q", a.Root)
	}
	d.arrive(0, a.DirStart)
	out := filepath.Join(t.TempDir(), "Game (1)")
	r := extract(t, extractor(t, a, d.f, out), d.missing)
	if len(r.Done) != 3 {
		t.Fatalf("done %v", r.Done)
	}
	for _, name := range []string{"bin/game.exe", "readme.txt"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "Game")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the root folder was nested: %v", err)
	}
}

func TestDoneEntriesAreSkippedAndLeftoversRedone(t *testing.T) {
	data := goZip(t, "",
		goFile{name: "a.txt", data: []byte("first")},
		goFile{name: "b.txt", data: []byte("second")},
	)
	d := newDownload(t, data)
	a := d.tail()
	d.arrive(0, a.DirStart)
	out := t.TempDir()
	// a.txt was done before: the user has since changed it, and that stays.
	if err := os.WriteFile(filepath.Join(out, "a.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	// b.txt was being written when the engine stopped.
	if err := os.WriteFile(filepath.Join(out, "b.txt.dms.part"), []byte("sec"), 0o644); err != nil {
		t.Fatal(err)
	}
	x := extractor(t, a, d.f, out, a.Entries[0].Start)
	if files, of := x.Progress(); files != 1 || of != 2 {
		t.Fatalf("progress %d of %d", files, of)
	}
	r := extract(t, x, d.missing)
	if len(r.Done) != 1 || r.Done[0].Name != "b.txt" {
		t.Fatalf("done %v", r.Done)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "a.txt")); string(b) != "mine" {
		t.Fatalf("a done Entry was written again: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "b.txt")); string(b) != "second" {
		t.Fatalf("b.txt is %q", b)
	}
	if l := leftovers(t, out); len(l) > 0 {
		t.Fatalf("left %v", l)
	}
	if r := extract(t, x, d.missing); len(r.Done) != 0 || len(r.Freed) != 0 {
		t.Fatalf("a second call did more: %v", r)
	}
}

func TestAnEntryDoneButNotSavedIsWrittenOverEvenReadOnly(t *testing.T) {
	data := goZip(t, "", goFile{name: "locked.txt", data: []byte("locked"), mode: 0o444})
	d := newDownload(t, data)
	a := d.tail()
	d.arrive(0, a.DirStart)
	out := t.TempDir()
	extract(t, extractor(t, a, d.f, out), d.missing)
	// The engine stopped before saving that it was done.
	d.arrive(0, a.DirStart)
	r := extract(t, extractor(t, a, d.f, out), d.missing)
	if len(r.Done) != 1 || len(r.Failed) != 0 {
		t.Fatalf("done %v, failed %v", r.Done, r.Failed)
	}
}

func TestAFileNamedLikeAnUnfinishedOneIsTurnedDown(t *testing.T) {
	// The archive holds a file named like DMS²'s unfinished files.
	data := goZip(t, "",
		goFile{name: "x.iso", data: []byte("a")},
		goFile{name: "x.iso.DMS.part", data: []byte("b")},
	)
	d := newDownload(t, data)
	a := d.tail()
	var u *Unsupported
	if _, err := NewExtractor(a, d.f, t.TempDir(), nil); !errors.As(err, &u) {
		t.Fatalf("NewExtractor: %v, want Unsupported", err)
	}
}

func TestAStoppedExtractionLeavesNothingHalfWritten(t *testing.T) {
	data := goZip(t, "", goFile{name: "a.bin", data: random(4 << 20)})
	d := newDownload(t, data)
	a := d.tail()
	d.arrive(0, a.DirStart)
	out := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := extractor(t, a, d.f, out).Extract(ctx, d.missing); !errors.Is(err, context.Canceled) {
		t.Fatalf("Extract: %v", err)
	}
	if l := leftovers(t, out); len(l) > 0 {
		t.Fatalf("left %v", l)
	}
	if d.allocatedIn(Range{a.Entries[0].Start, a.Entries[0].End}) == 0 {
		t.Fatal("bytes freed though nothing was extracted")
	}
}

func TestAFolderMadeForAPathCantTakeAnUnfinishedName(t *testing.T) {
	data := goZip(t, "",
		goFile{name: "x", data: []byte("a")},
		goFile{name: "x.dms.part/y.txt", data: []byte("b")},
	)
	d := newDownload(t, data)
	a := d.tail()
	var u *Unsupported
	if _, err := NewExtractor(a, d.f, t.TempDir(), nil); !errors.As(err, &u) {
		t.Fatalf("NewExtractor: %v, want Unsupported", err)
	}
}

func TestTheRootIsStrippedWhateverItsCase(t *testing.T) {
	// The Kelvin sign folds to K but takes three bytes.
	data := goZip(t, "",
		goFile{name: "Kit/a.txt", data: []byte("a")},
		goFile{name: "Kit/b.txt", data: []byte("b")},
	)
	d := newDownload(t, data)
	a := d.tail()
	if a.Root == "" {
		t.Fatal("no root")
	}
	d.arrive(0, a.DirStart)
	out := t.TempDir()
	extract(t, extractor(t, a, d.f, out), d.missing)
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestOverlappingMissingRangesStillCount(t *testing.T) {
	data := goZip(t, "", goFile{name: "a.bin", data: random(100 << 10), store: true})
	d := newDownload(t, data)
	a := d.tail()
	d.arrive(0, a.DirStart)
	e := a.Entries[0]
	// The middle is missing, inside a range that hides it from a search.
	missing := []Range{{0, e.End - 10}, {e.Start + 10, e.Start + 20}, {e.End + 1<<30, e.End + 2<<30}}
	r := extract(t, extractor(t, a, d.f, t.TempDir()), missing)
	if len(r.Done)+len(r.Failed) != 0 {
		t.Fatalf("extracted an Entry with missing bytes: done %v, failed %v", r.Done, r.Failed)
	}
}
