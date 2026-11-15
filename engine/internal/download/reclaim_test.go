package download

import (
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/archive"
)

// looks counts partInfo calls: each clones the part's whole state.
type looks struct {
	fakePart
	n int
}

func (p *looks) partInfo() partInfo { p.n++; return p.fakePart.partInfo() }

// With 100k Entries and half done, a resume looks at each part once and
// fetches again only what a stopped Entry had freed (ADR 0006).
func TestReclaimLooksAtAPartOnceForAHundredThousandEntries(t *testing.T) {
	const n, each = 100_000, 1 << 10
	arc := &archive.Archive{}
	var done []int64
	for i := range n {
		s := int64(i) * each
		arc.Entries = append(arc.Entries, archive.Entry{Name: "f", Size: each, Start: s, End: s + each})
		if i < n/2 {
			done = append(done, s)
		}
	}
	p := &looks{fakePart: fakePart{f: Folder{On: true, Done: done}, size: n * each, prefer: -1}}
	// The first Entry not done had its bytes freed by the stopped run.
	p.f.Freed = []Range{{n / 2 * each, n/2*each + each}}
	e := &Extraction{who: "test", arc: arc, parts: archive.NewParts([]int64{n * each})}

	start := time.Now()
	e.reclaim([]archivePart{p}, done)
	t.Logf("reclaim of %d Entries: %v", n, time.Since(start))
	if p.n != 1 {
		t.Fatalf("partInfo called %d times", p.n)
	}
	if len(p.refetched) != 1 || p.refetched[0].Start != n/2*each {
		t.Fatalf("fetched again %v", p.refetched)
	}

	p.f.Freed, p.n = nil, 0
	e.reclaim([]archivePart{p}, done)
	if p.n != 1 || len(p.refetched) != 1 {
		t.Fatalf("with nothing freed: %d looks, %d more fetches", p.n, len(p.refetched)-1)
	}
}
