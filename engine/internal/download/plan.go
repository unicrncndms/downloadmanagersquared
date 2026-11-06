package download

import (
	"math"
	"slices"
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
	// With them, claim sends a new connection to an Entry's front and cuts on
	// boundaries.
	bounds []int64
}

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
	if s, at := p.entryFront(newSpeed); s != nil {
		return p.split(s, at)
	}
	return p.split(victim, p.nearBound(victim, balancedCut(victim, newSpeed)))
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

// entryFront finds the earliest Entry front that can be split off a chunk
// being fetched, for a Download as folder: at least minSplit on each side,
// a chunk with 3 s or more left, and no further in than the balanced cut, so
// the new connection gets at least its share. None without Entry boundaries.
func (p *plan) entryFront(newSpeed float64) (*segment, int64) {
	var into *segment
	var at int64
	for _, s := range p.segs {
		if !canSplit(s) || timeLeft(s) < 3 {
			continue
		}
		i, _ := slices.BinarySearch(p.bounds, s.pos+minSplit)
		if i == len(p.bounds) {
			continue
		}
		if b := p.bounds[i]; b <= balancedCut(s, newSpeed) && (into == nil || b < at) {
			into, at = s, b
		}
	}
	return into, at
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

// wrote records the k bytes take reserved at off as on disk.
func (p *plan) wrote(s *segment, off, k int64) { s.written = off + k }

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
