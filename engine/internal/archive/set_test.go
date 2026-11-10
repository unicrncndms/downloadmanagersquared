package archive

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"testing"
)

// split-solid.7z.001… and split-nonsolid.7z.001… are solid.7z's Game folder
// packed again by 7-Zip with -v100k (-ms=on and -ms=off): three parts each.
func splitParts(t *testing.T, name string) [][]byte {
	t.Helper()
	var parts [][]byte
	for i := 1; i <= 3; i++ {
		parts = append(parts, testdata7z(t, fmt.Sprintf("%s.%03d", name, i)))
	}
	return parts
}

func TestASplit7zIsLaidOutFromItsFirstPart(t *testing.T) {
	parts := splitParts(t, "split-solid.7z")
	sizes, err := SplitSizes(parts[0][:sevenStartLen], int64(len(parts[0])))
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{int64(len(parts[0])), int64(len(parts[1])), int64(len(parts[2]))}
	if !slices.Equal(sizes, want) {
		t.Fatalf("sizes %v, want %v", sizes, want)
	}
	if _, err := SplitSizes([]byte("not a 7z at all, not even close to one"), 100); err == nil {
		t.Fatal("not a 7z laid out")
	}
}

func TestReadingAMissingPartSaysWhichOne(t *testing.T) {
	parts := splitParts(t, "split-solid.7z")
	p := NewParts([]int64{int64(len(parts[0])), int64(len(parts[1])), int64(len(parts[2]))})
	d := newDownload(t, parts[0])
	d.arrive(0, int64(len(parts[0])))
	p.Set(0, Sparse(d.f))
	b := make([]byte, 10)
	var m *MissingPart
	if _, err := p.ReadAt(b, int64(len(parts[0]))-5); !errors.As(err, &m) || m.Part != 2 {
		t.Fatalf("%v, want part 2 missing", err)
	}
}

// The parts arrive in any order, each in chunks in any order; the archive is
// read from the first part's front and the last one's tail, and every part's
// bytes are freed once extracted.
func TestASplit7zIsExtractedAcrossItsPartsAndFreesThemAll(t *testing.T) {
	t.Run("whole blocks", func(t *testing.T) { splitExtract(t) })
	t.Run("large blocks streamed", func(t *testing.T) {
		lowerLarge(t, 1<<10, 16<<10)
		splitExtract(t)
	})
}

func splitExtract(t *testing.T) {
	for _, name := range []string{"split-solid.7z", "split-nonsolid.7z"} {
		parts := splitParts(t, name)
		var sizes []int64
		var whole []byte
		for _, b := range parts {
			sizes = append(sizes, int64(len(b)))
			whole = append(whole, b...)
		}
		p := NewParts(sizes)
		ds := make([]*download, len(parts))
		for i, b := range parts {
			ds[i] = newDownload(t, b)
		}
		missing := func() []Range {
			var all []Range
			for i, d := range ds {
				if d == nil || p.files[i] == nil {
					all = append(all, p.Span(i))
					continue
				}
				for _, r := range d.missing {
					all = append(all, Range{r.Start + p.starts[i], r.End + p.starts[i]})
				}
			}
			return all
		}
		// Part 1 and the last part's tail first: what the list of files needs.
		ds[0].arrive(0, sevenStartLen)
		p.Set(0, Sparse(ds[0].f))
		last := len(parts) - 1
		ds[last].arrive(0, sizes[last])
		p.Set(last, Sparse(ds[last].f))
		a, err := Read(p, int64(len(whole)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out := t.TempDir()
		x, err := NewExtractorFrom(a, p, out, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer x.Close()

		type chunk struct {
			part int
			r    Range
		}
		var chunks []chunk
		for i := range last {
			for off := int64(0); off < sizes[i]; off += 8 << 10 {
				chunks = append(chunks, chunk{i, Range{off, min(off+8<<10, sizes[i])}})
			}
		}
		rand.New(rand.NewSource(3)).Shuffle(len(chunks), func(i, j int) { chunks[i], chunks[j] = chunks[j], chunks[i] })
		var freed []Range
		for _, c := range chunks {
			ds[c.part].arrive(c.r.Start, c.r.End)
			p.Set(c.part, Sparse(ds[c.part].f)) // the part's download has its file
			r := extract(t, x, missing())
			if len(r.Failed) > 0 {
				t.Fatalf("%s: failed: %v", name, r.Failed[0])
			}
			freed = append(freed, r.Freed...)
		}
		if !x.Finished() {
			t.Fatalf("%s: not finished", name)
		}
		var blocks []Range
		for _, e := range a.Entries {
			blocks = append(blocks, Range{e.Start, e.End})
		}
		if got, want := merge(freed), merge(blocks); !slices.Equal(got, want) {
			t.Fatalf("%s: freed %v, want every block %v", name, got, want)
		}
		for i, d := range ds {
			if n := d.allocatedIn(Range{0, sizes[i]}); n > 64<<10 {
				t.Fatalf("%s: part %d still holds %d bytes", name, i+1, n)
			}
		}
		checkExtracted(t, name, out, want7z(t, whole))
		if !bytes.Equal(whole[:6], sevenSig) {
			t.Fatal("not a 7z")
		}
	}
}

// splitwr.z01, .z02, .zip (WinRAR 7 -afzip -v100k) is a split ZIP: offsets
// are from each part's start. split7.zip.001… (7-Zip -tzip -v100k) is a ZIP
// cut in pieces. Both are the Game folder, and extract across their parts.
func TestASplitZipIsReadAcrossItsParts(t *testing.T) {
	for name, files := range map[string][]string{
		"splitwr.zip":    {"splitwr.z01", "splitwr.z02", "splitwr.zip"},
		"split7.zip.00x": {"split7.zip.001", "split7.zip.002", "split7.zip.003"},
	} {
		var sizes []int64
		var ds []*download
		for _, f := range files {
			b := testdata7z(t, f)
			sizes = append(sizes, int64(len(b)))
			ds = append(ds, newDownload(t, b))
		}
		if name == "splitwr.zip" {
			last := ds[2].data
			if n, err := ZipParts(last[len(last)-min(len(last), 1<<10):]); err != nil || n != 3 {
				t.Fatalf("%s: %d parts, %v", name, n, err)
			}
		}
		p := NewParts(sizes)
		for i, d := range ds {
			d.arrive(0, sizes[i])
			p.Set(i, Sparse(d.f))
		}
		total, _ := p.Size()
		if _, err := Read(ds[2].f, sizes[2]); name == "splitwr.zip" && !errors.Is(err, errSplit) {
			t.Fatalf("%s: its .zip read alone: %v", name, err)
		}
		a, err := Read(p, total)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out := t.TempDir()
		x, err := NewExtractorFrom(a, p, out, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer x.Close()
		if r := extract(t, x, nil); len(r.Failed) > 0 || !x.Finished() {
			t.Fatalf("%s: failed %v, finished %v", name, r.Failed, x.Finished())
		}
		checkExtracted(t, name, out, want7z(t, testdata7z(t, "solid.7z")))
	}
}
