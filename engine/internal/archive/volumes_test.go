package archive

import (
	"fmt"
	"testing"
	"time"
)

// volume.part1.rar… (RAR5, WinRAR 7) and rar4-volume.part1.rar… (RAR4,
// WinRAR 6.24 -ma4) are the Game folder in three -v100k volumes; files run
// across the volumes' boundaries.
func TestARarInVolumesIsExtractedAcrossThemAsTheyAreLaidOut(t *testing.T) {
	lowerLarge(t, 1<<10, 16<<10)
	for _, name := range []string{"volume.part%d.rar", "rar4-volume.part%d.rar"} {
		var ds []*download
		var sizes []int64
		for i := 1; i <= 3; i++ {
			b := testdata7z(t, fmt.Sprintf(name, i))
			ds = append(ds, newDownload(t, b))
			sizes = append(sizes, int64(len(b)))
		}
		// Only part 1 is known at first: the others are laid out as they come.
		p := NewParts(sizes[:1])
		missing := func() []Range {
			var all []Range
			for i := range p.Len() {
				s := p.Span(i)
				for _, r := range ds[i].missing {
					all = append(all, Range{r.Start + s.Start, r.End + s.Start})
				}
			}
			return append(all, Range{p.total(), OpenEnd})
		}
		ds[0].arrive(0, min(HeadLen, sizes[0]))
		p.Set(0, Sparse(ds[0].f))
		a, err := ReadVolumes(p, "")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out := t.TempDir()
		x, err := NewExtractorFrom(a, p, out, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer x.Close()
		var freed int64
		for i := range 3 {
			if i > 0 {
				p.Grow(sizes[i])
				p.Set(i, Sparse(ds[i].f))
			}
			for off := int64(0); off < sizes[i]; off += 8 << 10 {
				ds[i].arrive(off, min(off+8<<10, sizes[i]))
				r := extract(t, x, missing())
				if len(r.Failed) > 0 {
					t.Fatalf("%s: failed: %v", name, r.Failed[0])
				}
				for _, f := range r.Freed {
					freed += f.End - f.Start
				}
			}
			if i < 2 {
				// The decompressor gets to the end of what is laid out and asks for the next part.
				for end := time.Now().Add(5 * time.Second); p.Waiting() != i+2; time.Sleep(10 * time.Millisecond) {
					if time.Now().After(end) {
						t.Fatalf("%s: waits for part %d, want %d", name, p.Waiting(), i+2)
					}
					extract(t, x, missing())
				}
			}
		}
		for end := time.Now().Add(5 * time.Second); !x.Finished(); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatalf("%s: never finished", name)
			}
			extract(t, x, missing())
		}
		if freed == 0 {
			t.Fatalf("%s: nothing freed while it was extracted", name)
		}
		for i, d := range ds {
			if n := d.allocatedIn(Range{0, sizes[i]}); n > 64<<10 {
				t.Fatalf("%s: part %d still holds %d bytes", name, i+1, n)
			}
		}
		checkRar(t, name, out)
	}
}
