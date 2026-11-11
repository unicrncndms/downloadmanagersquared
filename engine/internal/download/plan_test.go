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

// An ordered plan (a Download as folder) hands a connection a stride's worth
// off the front of what is missing, and the next one what follows: not the
// far half of the file. Each chunk ends where the next begins, so it is
// fetched to its end and its connection kept.
func TestOrderedPlanKeepsConnectionsNearTheFront(t *testing.T) {
	size := int64(1000 * minSplit)
	p := newPlan(size, true, []Range{{0, size}})
	p.ordered = true
	a := p.claim(1 << 20) // 1 MiB/s: 10 MiB within the stride
	if a == nil || a.pos != 0 || a.end != stride<<20 {
		t.Fatalf("first chunk %+v, want the first %d bytes", a, stride<<20)
	}
	owned(a, 1<<20)
	b := p.claim(2 << 20)
	if b == nil || b.pos != a.end || b.end != 3*stride<<20 {
		t.Fatalf("second chunk %+v, want %d-%d", b, a.end, 3*stride<<20)
	}
	owned(b, 2<<20)
	// A speed not known yet: the smallest chunk.
	if c := p.claim(0); c == nil || c.pos != b.end || c.end != b.end+minSplit {
		t.Fatalf("third chunk %+v, want %d-%d", c, b.end, b.end+minSplit)
	}
}

// The cut lands on an Entry boundary when one is within minSplit of it.
func TestOrderedPlanCutsOnANearbyEntryBoundary(t *testing.T) {
	size := int64(1000 * minSplit)
	p := newPlan(size, true, []Range{{0, size}})
	p.ordered = true
	near := int64(stride<<20) + minSplit/2
	p.bounds = []int64{0, near}
	if got := p.claim(1 << 20); got == nil || got.end != near {
		t.Fatalf("chunk %+v, want it to end on the boundary %d", got, near)
	}
}
