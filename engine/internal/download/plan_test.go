package download

import (
	"reflect"
	"slices"
	"strings"
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
	p := newPlan(2*minTake-1, true, []Range{{0, 2*minTake - 1}})
	owned(p.claim(0), 1)
	if p.claim(0) != nil {
		t.Fatal("split a chunk smaller than 2*minTake")
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
// fetched to its end and its connection kept. A slow or unknown connection
// still gets orderedStride, so a big file makes few requests.
func TestOrderedPlanKeepsConnectionsNearTheFront(t *testing.T) {
	size := int64(1000 * orderedStride)
	p := newPlan(size, true, []Range{{0, size}})
	p.ordered = true
	a := p.claim(2 << 20) // 2 MiB/s: 20 MiB within the stride, so the floor
	if a == nil || a.pos != 0 || a.end != orderedStride {
		t.Fatalf("first chunk %+v, want the first %d bytes", a, orderedStride)
	}
	owned(a, 2<<20)
	b := p.claim(8 << 20) // 80 MiB within the stride
	if b == nil || b.pos != a.end || b.end != a.end+stride*8<<20 {
		t.Fatalf("second chunk %+v, want %d-%d", b, a.end, a.end+stride*8<<20)
	}
	owned(b, 8<<20)
	// A speed not known yet: the floor.
	if c := p.claim(0); c == nil || c.pos != b.end || c.end != b.end+orderedStride {
		t.Fatalf("third chunk %+v, want %d-%d", c, b.end, b.end+orderedStride)
	}
}

// The cut lands on an Entry boundary when one is within minSplit of it.
func TestOrderedPlanCutsOnANearbyEntryBoundary(t *testing.T) {
	size := int64(1000 * orderedStride)
	p := newPlan(size, true, []Range{{0, size}})
	p.ordered = true
	near := int64(orderedStride) + minSplit/2
	p.bounds = []int64{0, near}
	if got := p.claim(1 << 20); got == nil || got.end != near {
		t.Fatalf("chunk %+v, want it to end on the boundary %d", got, near)
	}
}

func TestAFileIsDrawnAsSquares(t *testing.T) {
	size := int64(mapCells * 100)
	w := &worker{iface: "eth"}
	p := newPlan(size, true, []Range{{250, 1000}, {4800, size}})
	p.segs[0].owner = w
	m, by := fileMap(size, p.ranges(), p.fetching(), nil, nil)
	want := "##0" + strings.Repeat(".", 7) + strings.Repeat("#", 38) + strings.Repeat(".", 48)
	if m != want || !slices.Equal(by, []string{"eth"}) {
		t.Fatalf("map %q by %v, want %q", m, by, want)
	}
	if m, _ := fileMap(-1, nil, nil, nil, nil); m != "" {
		t.Fatalf("a file of unknown size has no map, got %q", m)
	}
	// Sizes that don't divide into the squares, and a tiny file, stay in range.
	for _, size := range []int64{1, 95, 97, 1e12 + 7} {
		if m, _ := fileMap(size, []Range{{0, size}}, []fetch{{size - 1, "a"}}, nil, nil); len(m) != mapCells {
			t.Fatalf("size %d: %d squares", size, len(m))
		}
	}
}

func TestTheSquaresOnDiskShowWhichConnectionWroteThem(t *testing.T) {
	size := int64(mapCells * 100)
	eth, wifi := &worker{iface: "eth"}, &worker{iface: "wifi"}
	p := newPlan(size, true, []Range{{0, size}})
	front, back := p.segs[0], p.split(p.segs[0], size/2)
	front.owner, back.owner = eth, wifi
	p.wrote(front, 0, 300)     // squares 0-2, all on disk
	p.wrote(back, size/2, 250) // squares 48-49 on disk, 50 half
	m, by := fileMap(size, p.ranges(), p.fetching(), p.landed, p.writers)
	if !slices.Equal(by, []string{"eth", "wifi"}) {
		t.Fatalf("by %v", by)
	}
	if got := m[:4]; got != "aaa0" {
		t.Fatalf("front %q, want aaa0: three squares by eth, then eth writing", got)
	}
	if got := m[48:52]; got != "bb1." {
		t.Fatalf("back %q, want bb1.: two by wifi, wifi writing, then missing", got)
	}
	// Bytes on disk from before this run: on disk, nobody known.
	if m, _ := fileMap(size, []Range{{100, size}}, nil, nil, nil); m[0] != '#' {
		t.Fatalf("resumed square %q, want #", m[0])
	}
}

func TestMapFillSaysHowFarEachSquareBeingWrittenIs(t *testing.T) {
	size := int64(mapCells * 1000)
	f := mapFill(size, []fetch{{0, "a"}, {1000 + 500, "b"}, {size - 1, "c"}})
	if len(f) != mapCells || f[0] != '0' || f[1] != '5' || f[mapCells-1] != '9' || f[2] != '0' {
		t.Fatalf("mapFill = %q", f)
	}
	if mapFill(0, nil) != "" {
		t.Fatal("a file of unknown size has a fill")
	}
}

// A square with some of its bytes on disk says so, with who wrote them.
func TestPartMapShowsPartlyWrittenSquares(t *testing.T) {
	const size = 9600
	// Squares are 100 bytes: 0 whole, 1 three quarters, 2 a tenth, 3 untouched (missing from 300).
	missing := []Range{{125, size}}
	landed := make([]uint8, mapCells)
	landed[1], landed[2] = 1, 2
	part, by := partMap(size, missing, landed, []string{"eth", "wifi"}, nil)
	if len(part) != 2*mapCells {
		t.Fatalf("len %d", len(part))
	}
	for i, want := range map[int]string{0: "0-", 1: "2a", 2: "0-", 3: "0-"} {
		if got := part[2*i : 2*i+2]; got != want {
			t.Errorf("square %d is %q, want %q", i, got, want)
		}
	}
	if len(by) != 1 || by[0] != "eth" {
		t.Errorf("by %v", by)
	}
	// Any written byte shows: one byte of a square is a tenth.
	part, _ = partMap(size, []Range{{1, size}}, nil, nil, nil)
	if part[0:2] != "1-" {
		t.Errorf("one byte written: %q", part[0:2])
	}
	if p, _ := partMap(-1, nil, nil, nil, nil); p != "" {
		t.Errorf("unknown size: %q", p)
	}
}

// A chunk that begins inside a square leaves the square's start missing: the
// front there is nine tenths through the square (mapFill) while only four
// tenths of it are on disk (partMap), which is what the pages must show.
func TestPartMapOfASquareAChunkBeginsIn(t *testing.T) {
	const size = 9600 // squares of 100 bytes; square 12 is [1200, 1300)
	// Chunk A ends at 1250, its front far behind at 100; chunk B begins there and has written up to 1290.
	missing := []Range{{100, 1250}, {1290, size}}
	fill := mapFill(size, []fetch{{100, "a"}, {1290, "b"}})
	part, _ := partMap(size, missing, nil, nil, nil)
	if fill[12] != '9' {
		t.Fatalf("front is %c tenths through square 12, want 9", fill[12])
	}
	if got := part[24:26]; got != "4-" {
		t.Fatalf("square 12 has %q on disk, want 4 tenths", got)
	}
}
