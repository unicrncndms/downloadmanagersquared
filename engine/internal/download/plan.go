package download

import "math"

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
		if s.owner == nil || s.end-s.pos < 2*minSplit {
			continue
		}
		if t := float64(s.end-s.pos) / math.Max(s.owner.speed, 1); t > worst {
			worst, victim = t, s
		}
	}
	if victim == nil || worst < 3 {
		return nil
	}
	vs := math.Max(victim.owner.speed, 1)
	vn := vs
	if newSpeed > 0 {
		vn = newSpeed
	}
	// Split so both halves should finish at about the same time.
	cut := victim.pos + int64(float64(victim.end-victim.pos)*vs/(vs+vn))
	cut = min(max(cut, victim.pos+minSplit), victim.end-minSplit)
	ns := &segment{pos: cut, written: cut, end: victim.end}
	victim.end = cut
	p.segs = append(p.segs, ns)
	return ns
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
			p.segs = append(p.segs, &segment{pos: at, written: at, end: s.end})
			s.end = at
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
