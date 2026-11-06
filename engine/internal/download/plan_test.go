package download

import (
	"reflect"
	"testing"
)

func owned(s *segment, speed float64) { s.owner = &worker{speed: speed} }

func TestPlanHandsOutFreeChunksLowestFirst(t *testing.T) {
	p := newPlan(100, true, []Range{{60, 100}, {0, 30}})
	if s := p.claim(0); s.written != 0 || s.end != 30 {
		t.Fatalf("got chunk %d-%d, want 0-30", s.written, s.end)
	}
}

func TestPlanSplitsSoBothHalvesFinishTogether(t *testing.T) {
	size := int64(100 * minSplit)
	p := newPlan(size, true, []Range{{0, size}})
	slow := p.claim(0)
	owned(slow, 1<<20)

	// The new connection is three times as fast, so it should get about three quarters.
	fast := p.claim(3 << 20)
	if fast == nil {
		t.Fatal("no split")
	}
	if got, want := fast.end-fast.pos, size*3/4; got < want-minSplit || got > want+minSplit {
		t.Fatalf("new chunk is %d bytes, want about %d", got, want)
	}
	if slow.end != fast.pos || fast.end != size {
		t.Fatalf("halves don't line up: %d-%d and %d-%d", slow.pos, slow.end, fast.pos, fast.end)
	}
}

func TestPlanDoesNotSplitSmallOrQuickChunks(t *testing.T) {
	p := newPlan(2*minSplit-1, true, []Range{{0, 2*minSplit - 1}})
	owned(p.claim(0), 1)
	if p.claim(0) != nil {
		t.Fatal("split a chunk smaller than 2*minSplit")
	}

	p = newPlan(100*minSplit, true, []Range{{0, 100 * minSplit}})
	owned(p.claim(0), 1e12) // finishes in well under 3s
	if p.claim(0) != nil {
		t.Fatal("split a chunk that is about to finish")
	}
}

func TestPlanRangesAfterProgress(t *testing.T) {
	size := int64(100 * minSplit)
	p := newPlan(size, true, []Range{{0, size}})
	a := p.claim(0)
	owned(a, 1)
	b := p.claim(0)
	a.written, b.written = 5, b.pos+7
	p.release(a)
	p.release(b)
	want := []Range{{5, a.end}, {b.written, size}}
	if got := p.ranges(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ranges %v, want %v", got, want)
	}
	if got := p.done(); got != 12 {
		t.Fatalf("done %d, want 12", got)
	}
}

func TestPlanWithoutRangesIsOneChunkThatStartsOver(t *testing.T) {
	p := newPlan(50, false, nil)
	s := p.claim(0)
	owned(s, 1)
	if p.claim(0) != nil {
		t.Fatal("split a download without range support")
	}
	s.written = 20
	p.release(s)
	if s.written != 0 || p.done() != 0 {
		t.Fatalf("kept %d bytes after letting go; there is no resuming without ranges", s.written)
	}
	if p.claim(0) != s || s.pos != 0 {
		t.Fatal("the chunk wasn't handed out again from byte 0")
	}
}

func TestPlanUnknownSizeEndsWithTheResponse(t *testing.T) {
	p := newPlan(-1, false, nil)
	s := p.claim(0)
	s.written = 1234
	if p.done() != 1234 || p.remaining() == 0 {
		t.Fatalf("done %d remaining %d while still streaming", p.done(), p.remaining())
	}
	p.endHere(s)
	p.release(s)
	p.prune()
	if p.remaining() != 0 || p.done() != 1234 {
		t.Fatalf("after the end: done %d remaining %d", p.done(), p.remaining())
	}
}

// A cut puts a chunk boundary at a byte, so the bytes from there can be
// claimed on their own; an owned chunk ends there and keeps the rest free.
func TestPlanCutMakesABoundaryAtAByte(t *testing.T) {
	p := newPlan(100, true, []Range{{0, 100}})
	p.cut(90)
	if got := p.ranges(); !reflect.DeepEqual(got, []Range{{0, 90}, {90, 100}}) {
		t.Fatalf("free chunk: %v", got)
	}

	p = newPlan(100, true, []Range{{0, 100}})
	s := p.claim(0)
	owned(s, 1)
	p.take(s, 10)
	p.cut(90)
	if s.end != 90 {
		t.Fatalf("owned chunk ends at %d, want 90", s.end)
	}
	if n := p.claim(0); n == nil || n.pos != 90 || n.end != 100 {
		t.Fatalf("the bytes after the cut aren't free: %+v", n)
	}

	// A byte already claimed or on disk needs no cut.
	before := len(p.segs)
	p.cut(5)
	if len(p.segs) != before {
		t.Fatal("cut a chunk below where its connection already is")
	}
}

// With a byte preferred, the free chunk from there is claimed first.
func TestPlanClaimsThePreferredBytesFirst(t *testing.T) {
	p := newPlan(100, true, []Range{{0, 100}})
	p.cut(90)
	p.prefer = 90
	s := p.claim(0)
	if s.pos != 90 || s.end != 100 {
		t.Fatalf("claimed %d-%d, want 90-100", s.pos, s.end)
	}
	owned(s, 1)
	if s := p.claim(0); s.pos != 0 {
		t.Fatalf("then claimed %d, want 0", s.pos)
	}
}

// Bytes given back are missing again and claimed like any others.
func TestPlanAddGivesBytesBack(t *testing.T) {
	p := newPlan(100, true, nil)
	if p.remaining() != 0 {
		t.Fatal("not empty")
	}
	p.add(Range{20, 30})
	if p.remaining() != 10 || p.done() != 90 {
		t.Fatalf("remaining %d, done %d", p.remaining(), p.done())
	}
	if s := p.claim(0); s.pos != 20 || s.end != 30 {
		t.Fatalf("claimed %d-%d", s.pos, s.end)
	}
}

// With Entry boundaries, a new connection goes to the front of the earliest
// Entry not yet being fetched, not the far end of the slowest chunk.
func TestPlanSendsANewConnectionToTheNextEntrysFront(t *testing.T) {
	size := int64(100 * minSplit)
	p := newPlan(size, true, []Range{{0, size}})
	p.bounds = []int64{0, 10 * minSplit, 30 * minSplit, 90 * minSplit}
	a := p.claim(0)
	owned(a, 1<<20)
	p.take(a, minSplit) // a is fetching the first Entry

	b := p.claim(1 << 20)
	if b == nil || b.pos != 10*minSplit || b.end != size || a.end != 10*minSplit {
		t.Fatalf("new chunk %+v, first chunk ends %d; want the second Entry's front", b, a.end)
	}
	owned(b, 1<<20)
	if c := p.claim(1 << 20); c == nil || c.pos != 30*minSplit {
		t.Fatalf("third connection got %+v, want the third Entry's front", c)
	}
}

// A large Entry is fetched from its front: a connection starts at its first
// byte, not in its middle.
func TestPlanFetchesALargeEntryFrontFirst(t *testing.T) {
	size := int64(100 * minSplit)
	p := newPlan(size, true, []Range{{0, size}})
	p.bounds = []int64{0, 2 * minSplit, size - 2*minSplit} // a large Entry in the middle
	a := p.claim(0)
	owned(a, 1<<20)
	p.take(a, minSplit/2)
	if b := p.claim(1 << 20); b == nil || b.pos != 2*minSplit {
		t.Fatalf("got %+v, want the large Entry's front at %d", b, 2*minSplit)
	}
}

// When no Entry front can be split off, the cut is speed-weighted as today but
// lands on an Entry boundary when one is within minSplit. An Entry front
// past today's cut would give the new connection less than its share, so
// the cut is today's, moved onto that boundary.
func TestPlanMovesACutOntoANearbyEntryBoundary(t *testing.T) {
	size := int64(100 * minSplit)
	plain := newPlan(size, true, []Range{{0, size}})
	owned(plain.claim(0), 1<<20)
	today := plain.claim(3 << 20).pos // about size/4: the new connection is 3x as fast

	p := newPlan(size, true, []Range{{0, size}})
	near := today + minSplit/2
	p.bounds = []int64{0, near}
	owned(p.claim(0), 1<<20)
	if got := p.claim(3 << 20); got == nil || got.pos != near {
		t.Fatalf("cut at %+v, want the boundary %d (today's cut %d)", got, near, today)
	}
}

// Far from any boundary, the cut is exactly today's.
func TestPlanKeepsTheCutWhenNoBoundaryIsNear(t *testing.T) {
	size := int64(100 * minSplit)
	plain := newPlan(size, true, []Range{{0, size}})
	owned(plain.claim(0), 1<<20)
	want := plain.claim(3 << 20)

	p := newPlan(size, true, []Range{{0, size}})
	p.bounds = []int64{0}
	owned(p.claim(0), 1<<20)
	if got := p.claim(3 << 20); got.pos != want.pos || got.end != want.end {
		t.Fatalf("cut %d-%d, want today's %d-%d", got.pos, got.end, want.pos, want.end)
	}
}
