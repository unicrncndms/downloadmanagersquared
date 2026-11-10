package archive

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bodgit/sevenzip"
)

// The test archives were made with 7-Zip 24 from a folder Game holding
// readme.txt, docs/manual.txt, data.bin (random), empty.txt and empty-dir:
// solid.7z (one LZMA2 block), nonsolid.7z (-ms=off: a block per file),
// plainheader.7z (-mhc=off: the list of files not compressed), bcj2.7z
// (-mf=BCJ2: a block reading four packed streams), and password.7z and
// password-header.7z (-psecret, the second with -mhe=on).
func testdata7z(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var sevenZips = []string{"solid.7z", "nonsolid.7z", "plainheader.7z", "bcj2.7z"}

// headAndTail fails any read outside the start header and the last n bytes,
// except the signature search's read of the front.
type headAndTail struct {
	b []byte
	n int64
}

func (r headAndTail) ReadAt(p []byte, off int64) (int, error) {
	end := off + int64(len(p))
	if off == 0 || end <= sevenStartLen || off >= int64(len(r.b))-r.n {
		return bytes.NewReader(r.b).ReadAt(p, off)
	}
	return 0, fmt.Errorf("read at %d, outside the head and the last %d bytes", off, r.n)
}

// want7z is what 7-Zip's own format says is in the archive, read with the
// sevenzip package straight from all its bytes.
func want7z(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	zr, err := sevenzip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	m := map[string][]byte{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		m[f.Name] = data
	}
	return m
}

func TestSevenZipNeedAndReadUseOnlyTheHeadAndTail(t *testing.T) {
	for _, name := range sevenZips {
		b := testdata7z(t, name)
		size := int64(len(b))
		// From a tail too short, as the first guess may be: Need widens it.
		have := int64(64)
		for range 8 {
			n, err := Need(headAndTail{b, have}, size, have)
			if err != nil {
				t.Fatalf("%s: Need: %v", name, err)
			}
			if n <= have {
				break
			}
			have = n
		}
		a, err := Read(headAndTail{b, have}, size)
		if err != nil {
			t.Fatalf("%s: Read with the tail Need asked for (%d bytes): %v", name, have, err)
		}
		if a.DirStart != size-have {
			t.Fatalf("%s: list starts at %d, Need asked from %d", name, a.DirStart, size-have)
		}
		if a.Root != "Game" {
			t.Fatalf("%s: root %q", name, a.Root)
		}
		at := int64(sevenStartLen)
		for _, e := range a.Entries {
			if e.Start != at || e.End <= e.Start {
				t.Fatalf("%s: block %q at [%d, %d), want it from %d", name, e.Name, e.Start, e.End, at)
			}
			at = e.End
		}
		if at != a.DirStart {
			t.Fatalf("%s: blocks end at %d, the list starts at %d", name, at, a.DirStart)
		}
		var files []string
		for _, f := range a.z.files {
			files = append(files, f.Name)
		}
		slices.Sort(files)
		if want := []string{"Game/", "Game/data.bin", "Game/docs/", "Game/docs/manual.txt", "Game/empty-dir/", "Game/empty.txt", "Game/readme.txt"}; !slices.Equal(files, want) {
			t.Fatalf("%s: files %v", name, files)
		}
	}
	if n := len(mustRead7z(t, "nonsolid.7z").Entries); n != 3 {
		t.Fatalf("nonsolid.7z: %d blocks, want 3 (one per file with bytes)", n)
	}
	if a := mustRead7z(t, "bcj2.7z"); !slices.Contains(a.z.multi, true) {
		t.Fatal("bcj2.7z: no block marked as reading several streams")
	}
}

func mustRead7z(t *testing.T, name string) *Archive {
	t.Helper()
	b := testdata7z(t, name)
	a, err := Read(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return a
}

func TestAPasswordProtected7zIsTurnedDown(t *testing.T) {
	for _, name := range []string{"password.7z", "password-header.7z"} {
		b := testdata7z(t, name)
		_, err := Read(bytes.NewReader(b), int64(len(b)))
		if !errors.Is(err, errPassword) {
			t.Fatalf("%s: %v, want the password reason", name, err)
		}
	}
}

func TestNotA7zIsTurnedDown(t *testing.T) {
	b := testdata7z(t, "solid.7z")
	b = slices.Clone(b)
	b[len(b)-10] ^= 0xff // the list of files' CRC no longer matches
	_, err := Read(bytes.NewReader(b), int64(len(b)))
	var u *Unsupported
	if !errors.As(err, &u) {
		t.Fatalf("a broken list: %v, want Unsupported", err)
	}
}

// arrive7z gives the download its head and tail, as the download fetches them
// first, and reads the list.
func arrive7z(t *testing.T, d *download) *Archive {
	t.Helper()
	size := int64(len(d.data))
	d.arrive(0, sevenStartLen)
	need, err := Need(bytes.NewReader(d.data), size, TailGuess)
	if err != nil {
		t.Fatal(err)
	}
	d.arrive(size-min(need, size), size)
	a, err := Read(d.f, size)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func checkExtracted(t *testing.T, name, out string, want map[string][]byte) {
	t.Helper()
	for p, data := range want {
		rel, _ := filepath.Rel("Game", filepath.FromSlash(p))
		got, err := os.ReadFile(filepath.Join(out, rel))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("%s: %s wrong (%v)", name, p, err)
		}
	}
	if fi, err := os.Stat(filepath.Join(out, "empty-dir")); err != nil || !fi.IsDir() {
		t.Fatalf("%s: empty-dir missing", name)
	}
	if l := leftovers(t, out); len(l) > 0 {
		t.Fatalf("%s: left %v", name, l)
	}
}

func TestSevenZipBlocksAreExtractedAndFreedAsTheirBytesArriveInAnyOrder(t *testing.T) {
	for _, name := range sevenZips {
		b := testdata7z(t, name)
		d := newDownload(t, b)
		a := arrive7z(t, d)
		out := t.TempDir()
		x := extractor(t, a, d.f, out)
		defer x.Close()

		var chunks []Range
		for off := int64(sevenStartLen); off < a.DirStart; off += 16 << 10 {
			chunks = append(chunks, Range{off, min(off+16<<10, a.DirStart)})
		}
		rand.New(rand.NewSource(1)).Shuffle(len(chunks), func(i, j int) { chunks[i], chunks[j] = chunks[j], chunks[i] })
		var freed []Range
		for _, c := range chunks {
			d.arrive(c.Start, c.End)
			r := extract(t, x, d.missing)
			if len(r.Failed) > 0 {
				t.Fatalf("%s: failed: %v", name, r.Failed[0])
			}
			for _, e := range r.Done {
				if !covered(d.missing, e) {
					t.Fatalf("%s: %s done before its bytes arrived", name, e.Name)
				}
			}
			freed = append(freed, r.Freed...)
		}
		if !x.Finished() {
			t.Fatalf("%s: not finished", name)
		}
		if files, of := x.Progress(); files != of || of != 4 {
			t.Fatalf("%s: progress %d of %d, want 4 of 4", name, files, of)
		}
		checkExtracted(t, name, out, want7z(t, b))
		if n := sumRanges(merge(freed)); n != a.DirStart-sevenStartLen {
			t.Fatalf("%s: freed %d bytes, the blocks take %d", name, n, a.DirStart-sevenStartLen)
		}
	}
}

// A large block (the whole of solid.7z) decompresses as its bytes arrive from
// its front, and what the decompressor has read is freed behind it.
func TestALargeSevenZipBlockIsExtractedInOrderAndFreedBehind(t *testing.T) {
	lowerLarge(t, 1<<10, 16<<10)
	b := testdata7z(t, "solid.7z")
	d := newDownload(t, b)
	a := arrive7z(t, d)
	if len(a.Entries) != 1 {
		t.Fatalf("%d blocks, want 1", len(a.Entries))
	}
	e := a.Entries[0]
	out := t.TempDir()
	x := extractor(t, a, d.f, out)
	defer x.Close()

	var freed []Range
	early := 0 // calls that freed bytes before the block was done
	for off := e.Start; !x.Finished(); off += 8 << 10 {
		if off < e.End {
			d.arrive(off, min(off+8<<10, e.End))
		}
		if off > e.End+(64<<20) {
			t.Fatal("never finished")
		}
		r := extract(t, x, d.missing)
		if len(r.Failed) > 0 {
			t.Fatalf("failed: %v", r.Failed[0])
		}
		freed = append(freed, r.Freed...)
		if len(r.Freed) > 0 && len(r.Done) == 0 {
			early++
		}
	}
	if sumRanges(merge(freed)) != e.End-e.Start {
		t.Fatalf("freed %d of the block's %d bytes", sumRanges(merge(freed)), e.End-e.Start)
	}
	// Small steps never free a whole 64 KB unit of a sparse file, so what is
	// allocated can't show it: the reports do.
	if early < 5 {
		t.Fatalf("only %d steps freed bytes behind the decompressor", early)
	}
	checkExtracted(t, "solid.7z", out, want7z(t, b))
}

func TestAWrongByteFailsTheSevenZipBlockAndLeavesNoFile(t *testing.T) {
	b := slices.Clone(testdata7z(t, "nonsolid.7z"))
	clean := testdata7z(t, "nonsolid.7z")
	d := newDownload(t, b)
	a := arrive7z(t, d)
	e := a.Entries[0]
	b[e.Start+(e.End-e.Start)/2] ^= 0x55
	out := t.TempDir()
	x := extractor(t, a, d.f, out)
	defer x.Close()
	d.arrive(e.Start, e.End)
	r := extract(t, x, d.missing)
	if len(r.Failed) != 1 || r.Failed[0].Range != (Range{e.Start, e.End}) || len(r.Done) > 0 {
		t.Fatalf("done %v, failed %v", r.Done, r.Failed)
	}
	if l := leftovers(t, out); len(l) > 0 {
		t.Fatalf("left %v", l)
	}
	// Fetched again, right this time.
	d.data = clean
	d.arrive(e.Start, e.End)
	if r := extract(t, x, d.missing); len(r.Done) != 1 {
		t.Fatalf("second try: done %v, failed %v", r.Done, r.Failed)
	}
}

func TestDoneSevenZipBlocksAreSkipped(t *testing.T) {
	b := testdata7z(t, "nonsolid.7z")
	d := newDownload(t, b)
	a := arrive7z(t, d)
	d.arrive(sevenStartLen, a.DirStart)
	out := t.TempDir()
	x := extractor(t, a, d.f, out, a.Entries[0].Start)
	defer x.Close()
	r := extract(t, x, d.missing)
	if len(r.Done) != 2 || slices.ContainsFunc(r.Done, func(e Entry) bool { return e.Start == a.Entries[0].Start }) {
		t.Fatalf("done %v", r.Done)
	}
	if !x.Finished() {
		t.Fatal("not finished")
	}
}

// Stopped halfway through a large block, nothing half-written is left and the
// decompressor ends; the next Extractor starts the block from its front.
func TestAStoppedSevenZipBlockLeavesNothingHalfWritten(t *testing.T) {
	lowerLarge(t, 1<<10, 16<<10)
	b := testdata7z(t, "solid.7z")
	d := newDownload(t, b)
	a := arrive7z(t, d)
	e := a.Entries[0]
	out := t.TempDir()
	x := extractor(t, a, d.f, out)
	d.arrive(e.Start, e.Start+(e.End-e.Start)/2)
	extract(t, x, d.missing)
	x.Close()
	if l := leftovers(t, out); len(l) > 0 {
		t.Fatalf("left %v", l)
	}
	d = newDownload(t, b)
	d.arrive(0, int64(len(b)))
	x = extractor(t, a, d.f, out)
	defer x.Close()
	extract(t, x, d.missing)
	if !x.Finished() {
		t.Fatal("not finished the second time")
	}
	checkExtracted(t, "solid.7z", out, want7z(t, b))
}
