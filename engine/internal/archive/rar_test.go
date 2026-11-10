package archive

import (
	"bytes"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The RAR test archives were made with WinRAR 7's Rar.exe (RAR5) from the
// same Game folder as the 7z ones: solid.rar (-s), nonsolid.rar,
// password.rar (-psecret), password-header.rar (-hpsecret) and
// volume.part1.rar (the first of three -v100k volumes).
var rars = []string{"solid.rar", "nonsolid.rar"}

func TestARarIsOneEntryReadFromItsFront(t *testing.T) {
	for _, name := range rars {
		b := testdata7z(t, name)
		size := int64(len(b))
		if n, err := Need(bytes.NewReader(b), size, TailGuess); err != nil || n != min(TailGuess, size) {
			t.Fatalf("%s: Need %d, %v", name, n, err)
		}
		a, err := Read(bytes.NewReader(b[:HeadLen]), size)
		if err != nil {
			t.Fatalf("%s: Read from its front only: %v", name, err)
		}
		if !a.rar || len(a.Entries) != 1 || a.Entries[0] != (Entry{Name: "the archive", Stored: "the archive", Method: methodRar, CompressedSize: size, Size: size, End: size}) || a.DirStart != size {
			t.Fatalf("%s: %+v", name, a)
		}
	}
}

func TestRarsDMSCantExtractAreTurnedDown(t *testing.T) {
	for name, want := range map[string]error{
		"password.rar": errPassword, "password-header.rar": errPassword, "volume.part1.rar": errSplit,
	} {
		b := testdata7z(t, name)
		if _, err := Read(bytes.NewReader(b), int64(len(b))); !errors.Is(err, want) {
			t.Fatalf("%s: %v, want %v", name, err, want)
		}
	}
	b := slices.Clone(testdata7z(t, "solid.rar"))
	b[len(rar5Sig)+4+2] = 9 // not a main header
	var u *Unsupported
	if _, err := Read(bytes.NewReader(b), int64(len(b))); !errors.As(err, &u) {
		t.Fatalf("a broken header: %v", err)
	}
}

// checkRar checks the extracted files against the same files in solid.7z.
// A RAR has no Root taken off: Game stays.
func checkRar(t *testing.T, name, out string) {
	t.Helper()
	for p, data := range want7z(t, testdata7z(t, "solid.7z")) {
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(p)))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("%s: %s wrong (%v)", name, p, err)
		}
	}
	if fi, err := os.Stat(filepath.Join(out, "Game", "empty-dir")); err != nil || !fi.IsDir() {
		t.Fatalf("%s: empty-dir missing", name)
	}
	if l := leftovers(t, out); len(l) > 0 {
		t.Fatalf("%s: left %v", name, l)
	}
}

func rarDownload(t *testing.T, name string) (*download, *Archive) {
	t.Helper()
	d := newDownload(t, testdata7z(t, name))
	size := int64(len(d.data))
	d.arrive(0, min(HeadLen, size))
	a, err := Read(d.f, size)
	if err != nil {
		t.Fatal(err)
	}
	return d, a
}

func TestASmallRarIsExtractedOnceItIsAllThere(t *testing.T) {
	for _, name := range rars {
		d, a := rarDownload(t, name)
		out := t.TempDir()
		x := extractor(t, a, d.f, out)
		defer x.Close()
		var chunks []Range
		for off := int64(HeadLen); off < int64(len(d.data)); off += 16 << 10 {
			chunks = append(chunks, Range{off, min(off+16<<10, int64(len(d.data)))})
		}
		rand.New(rand.NewSource(2)).Shuffle(len(chunks), func(i, j int) { chunks[i], chunks[j] = chunks[j], chunks[i] })
		for i, c := range chunks {
			d.arrive(c.Start, c.End)
			r := extract(t, x, d.missing)
			if len(r.Failed) > 0 {
				t.Fatalf("%s: failed: %v", name, r.Failed[0])
			}
			if last := i == len(chunks)-1; last != (len(r.Done) == 1) {
				t.Fatalf("%s: done %v after %d of %d chunks", name, r.Done, i+1, len(chunks))
			}
		}
		if !x.Finished() {
			t.Fatalf("%s: not finished", name)
		}
		if files, of := x.Progress(); files != 4 || of != 4 {
			t.Fatalf("%s: progress %d of %d, want 4 of 4", name, files, of)
		}
		checkRar(t, name, out)
	}
}

// A large RAR decompresses as its bytes arrive from its front, and what the
// decompressor has read is freed behind it.
func TestALargeRarIsExtractedInOrderAndFreedBehind(t *testing.T) {
	lowerLarge(t, 1<<10, 16<<10)
	for _, name := range rars {
		d, a := rarDownload(t, name)
		size := int64(len(d.data))
		out := t.TempDir()
		x := extractor(t, a, d.f, out)
		defer x.Close()
		var freed []Range
		early, unknown := 0, false
		for off := int64(HeadLen); !x.Finished(); off += 8 << 10 {
			if off < size {
				d.arrive(off, min(off+8<<10, size))
			}
			if off > size+(64<<20) {
				t.Fatalf("%s: never finished", name)
			}
			r := extract(t, x, d.missing)
			if len(r.Failed) > 0 {
				t.Fatalf("%s: failed: %v", name, r.Failed[0])
			}
			freed = append(freed, r.Freed...)
			if len(r.Freed) > 0 && len(r.Done) == 0 {
				early++
			}
			if _, of := x.Progress(); of == 0 && !x.Finished() {
				unknown = true
			}
		}
		if n := sumRanges(merge(freed)); n != size {
			t.Fatalf("%s: freed %d of %d bytes", name, n, size)
		}
		if early < 5 || !unknown {
			t.Fatalf("%s: %d steps freed bytes early; of unknown while running: %v", name, early, unknown)
		}
		checkRar(t, name, out)
	}
}

func TestAWrongByteFailsTheRar(t *testing.T) {
	d, a := rarDownload(t, "nonsolid.rar")
	clean := slices.Clone(d.data)
	d.data[len(d.data)/2] ^= 0x55
	out := t.TempDir()
	x := extractor(t, a, d.f, out)
	defer x.Close()
	d.arrive(HeadLen, int64(len(d.data)))
	r := extract(t, x, d.missing)
	if len(r.Failed) != 1 || len(r.Done) > 0 {
		t.Fatalf("done %v, failed %v", r.Done, r.Failed)
	}
	if l := leftovers(t, out); len(l) > 0 {
		t.Fatalf("left %v", l)
	}
	d.data = clean
	d.arrive(0, int64(len(d.data)))
	if r := extract(t, x, d.missing); len(r.Done) != 1 {
		t.Fatalf("second try: done %v, failed %v", r.Done, r.Failed)
	}
	checkRar(t, "nonsolid.rar", out)
}
