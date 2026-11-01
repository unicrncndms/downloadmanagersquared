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
