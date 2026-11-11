package download

import (
	"math"
	"slices"
	"strings"
)

// unknownEnd is the end of a file whose size the server didn't say; the chunk
// ends wherever the response does.
const unknownEnd = math.MaxInt64

// segment is a chunk of the file that one worker fetches at a time.
type segment struct {
	pos     int64 // next byte to fetch (claimed)
	written int64 // bytes before this are on disk
	end     int64 // exclusive
	owner   *worker
}

// plan tracks which bytes of the file are still missing and who is fetching
// them. Without range support it is a single chunk that can't be split and
// starts over from byte 0 whenever its connection lets go of it.
type plan struct {
	size       int64 // -1 while unknown
	splittable bool
	segs       []*segment

	// prefer is a byte whose free chunk is claimed before any other (a
	// Download as folder's tail), -1 for none.
	prefer int64

	// bounds are a Download as folder's Entry boundaries in byte order (each
	// Entry's start, and the central directory's), nil for any other download.
	// With them, an ordered plan cuts on a boundary when one is near.
	bounds []int64

	// landed is which interface wrote each square of the file's map
	// (fileMap), as 1 + its place in writers; 0 where none is known (bytes on
	// disk before this run). Only memory: a resumed file starts without.
	landed  []uint8
	writers []string

	// ordered: a Download as folder, or a part of one. Its bytes are extracted
	// and freed front to back, so they are fetched that way too: a connection
	// takes only a stride's worth off the front of what is missing, so all of
	// them work close to the front and little waits on disk for the
	// extraction to reach it. A chunk is asked for exactly, to its end, so its
	// connection is kept for the next one: ordering costs no speed.
	ordered bool
}

// stride is how many seconds of fetching a connection's chunk is in an
// ordered plan: what is on disk ahead of the front stays within about this
// much of the whole download's speed.
const stride = 10

func newPlan(size int64, rangeOK bool, remaining []Range) *plan {
	p := &plan{size: size, splittable: rangeOK, prefer: -1}
	if !rangeOK {
		end := size
		if end < 0 {
			end = unknownEnd
		}
		p.segs = []*segment{{end: end}}
		return p
	}
	for _, r := range remaining {
		p.segs = append(p.segs, &segment{pos: r.Start, written: r.Start, end: r.End})
	}
	return p
}

// claim hands out work: a free chunk if there is one, otherwise the far end of
// the chunk that would take longest to finish. newSpeed is what the new
// connection is expected to manage (bytes/s), 0 if unknown.
func (p *plan) claim(newSpeed float64) *segment {
	var free *segment
	for _, s := range p.segs {
		if s.owner == nil && s.written < s.end && (free == nil || p.before(s, free)) {
			free = s
		}
	}
	if free != nil {
		free.pos = free.written
		if p.ordered && p.splittable {
			// Only its front: the rest stays free, for the next connection.
			at := free.pos + max(int64(newSpeed*stride), minSplit)
			if at <= free.end-minSplit {
				at = p.nearBound(free, at)
				p.segs = append(p.segs, &segment{pos: at, written: at, end: free.end})
				free.end = at
			}
		}
		return free
	}
	if !p.splittable {
		return nil
	}

	var victim *segment
	var worst float64
	for _, s := range p.segs {
		if !canSplit(s) {
			continue
		}
		if t := timeLeft(s); t > worst {
			worst, victim = t, s
		}
	}
	if victim == nil || worst < 3 {
		return nil
	}
	return p.split(victim, balancedCut(victim, newSpeed))
}

// canSplit: a chunk is being fetched and big enough to split in two.
func canSplit(s *segment) bool { return s.owner != nil && s.end-s.pos >= 2*minSplit }

// timeLeft is how long a chunk's connection needs for the rest of it, in seconds.
func timeLeft(s *segment) float64 { return float64(s.end-s.pos) / math.Max(s.owner.speed, 1) }

// balancedCut is where to split a chunk so both halves should finish at about
// the same time, keeping each at least minSplit.
func balancedCut(s *segment, newSpeed float64) int64 {
	vs := math.Max(s.owner.speed, 1)
	vn := vs
	if newSpeed > 0 {
		vn = newSpeed
	}
	cut := s.pos + int64(float64(s.end-s.pos)*vs/(vs+vn))
	return min(max(cut, s.pos+minSplit), s.end-minSplit)
}

// split hands out the bytes of a chunk from at on, as a new chunk.
func (p *plan) split(s *segment, at int64) *segment {
	ns := &segment{pos: at, written: at, end: s.end}
	s.end = at
	p.segs = append(p.segs, ns)
	return ns
}

// nearBound moves a cut in chunk s onto the closest Entry boundary within
// minSplit of it, if one keeps both halves at least minSplit.
func (p *plan) nearBound(s *segment, cut int64) int64 {
	best := cut
	i, _ := slices.BinarySearch(p.bounds, cut)
	for _, j := range []int{i - 1, i} {
		if j < 0 || j >= len(p.bounds) {
			continue
		}
		b := p.bounds[j]
		if b < s.pos+minSplit || b > s.end-minSplit || abs(b-cut) >= minSplit {
			continue
		}
		if best == cut || abs(b-cut) < abs(best-cut) {
			best = b
		}
	}
	return best
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// before: free chunk a is claimed before b, the preferred one first, then the lowest.
func (p *plan) before(a, b *segment) bool {
	if p.prefer >= 0 {
		pa, pb := a.written >= p.prefer, b.written >= p.prefer
		if pa != pb {
			return pa
		}
	}
	return a.written < b.written
}

// cut makes a chunk boundary at byte at, so the missing bytes from there can
// be claimed on their own. A chunk being fetched past at ends there instead,
// as when claim splits it. Bytes already claimed or on disk need no cut.
func (p *plan) cut(at int64) {
	for _, s := range p.segs {
		if s.pos < at && at < s.end {
			p.split(s, at)
			return
		}
	}
}

// add makes bytes that were on disk missing again, to be fetched once more.
// None of them may be missing already.
func (p *plan) add(r Range) {
	if r.Start < r.End {
		p.segs = append(p.segs, &segment{pos: r.Start, written: r.Start, end: r.End})
	}
}

// release frees a chunk its worker has stopped on, so it can be claimed again.
func (p *plan) release(s *segment) {
	s.owner = nil
	if !p.splittable && s.written < s.end {
		s.written = 0 // no ranges, so no resuming either
	}
	s.pos = s.written
}

// take reserves the next n bytes of a chunk for its worker to write, or fewer
// if the chunk's end has moved down since (another connection took it over).
// k < n means the worker's chunk now ends at off+k.
func (p *plan) take(s *segment, n int64) (off, k int64) {
	k = max(min(n, s.end-s.pos), 0)
	off = s.pos
	s.pos += k
	return off, k
}

// wrote records the k bytes take reserved at off as on disk, and which
// interface wrote them.
func (p *plan) wrote(s *segment, off, k int64) {
	s.written = off + k
	if p.size > 0 && k > 0 && s.owner != nil {
		p.mark(off, off+k, s.owner.iface)
	}
}

// mark notes iface as the writer of the squares bytes from to to fall in.
func (p *plan) mark(from, to int64, iface string) {
	w := slices.Index(p.writers, iface)
	if w < 0 {
		if len(p.writers) == 255 {
			return
		}
		p.writers, w = append(p.writers, iface), len(p.writers)
	}
	if p.landed == nil {
		p.landed = make([]uint8, mapCells)
	}
	for c := from * mapCells / p.size; c <= (to-1)*mapCells/p.size && c < mapCells; c++ {
		p.landed[c] = uint8(w + 1)
	}
}

// endHere settles a chunk of unknown length: the response has ended, so the file has.
func (p *plan) endHere(s *segment) {
	s.end = s.written
	p.size = s.written
}

func (p *plan) remaining() int64 {
	var n int64
	for _, s := range p.segs {
		n += s.end - s.written
	}
	return n
}

// done is how many bytes of the file are on disk.
func (p *plan) done() int64 {
	if p.size >= 0 {
		return p.size - p.remaining()
	}
	return p.segs[0].written // a single chunk of unknown length
}

// ranges is what is still missing, for saving progress.
func (p *plan) ranges() []Range {
	var rem []Range
	for _, s := range p.segs {
		if s.written < s.end {
			rem = append(rem, Range{s.written, s.end})
		}
	}
	return rem
}

// prune drops finished chunks so the list stays short.
func (p *plan) prune() {
	kept := p.segs[:0]
	for _, s := range p.segs {
		if s.owner != nil || s.written < s.end {
			kept = append(kept, s)
		}
	}
	p.segs = kept
}

// mapCells is how many squares the pages draw a file as.
const mapCells = 96

// fetch is a byte being written right now, and the interface writing it.
type fetch struct {
	pos   int64
	iface string
}

// fetching is where each chunk with a connection is being written.
func (p *plan) fetching() []fetch {
	var now []fetch
	for _, s := range p.segs {
		if s.owner != nil && s.written < s.end {
			now = append(now, fetch{s.written, s.owner.iface})
		}
	}
	return now
}

// fileMap draws a file of size bytes as mapCells characters, for the pages'
// grid of squares: '#' all on disk, '.' some of it missing, a letter where
// all of it is on disk and an interface wrote it (a for by[0], b for by[1]…),
// and a digit where a connection is writing right now (0 for by[0]…). landed
// and writers are the plan's record of who wrote what, nil when there is
// none. "" for a file of unknown size.
func fileMap(size int64, missing []Range, now []fetch, landed []uint8, writers []string) (cells string, by []string) {
	if size <= 0 {
		return "", nil
	}
	cell := func(b int64) int { return int(min(b*mapCells/size, mapCells-1)) }
	m := []byte(strings.Repeat("#", mapCells))
	for _, r := range missing {
		if r.Start >= r.End {
			continue
		}
		for i := cell(r.Start); i <= cell(r.End-1); i++ {
			m[i] = '.'
		}
	}
	// by's place for iface, or -1 once ten are taken (a digit each).
	place := func(iface string) int {
		k := slices.Index(by, iface)
		if k < 0 && len(by) < 10 {
			by, k = append(by, iface), len(by)
		}
		return k
	}
	for i, w := range landed {
		if m[i] == '#' && w > 0 && int(w) <= len(writers) {
			if k := place(writers[w-1]); k >= 0 {
				m[i] = byte('a' + k)
			}
		}
	}
	for _, f := range now {
		if k := place(f.iface); k >= 0 {
			m[cell(f.pos)] = byte('0' + k)
		}
	}
	return string(m), by
}
