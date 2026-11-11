package download

import (
	"math/rand"
	"testing"
)

// simEntries lays out an archive of 300 Entries of up to 6 MiB, with every
// sixtieth one big instead. It returns each Entry's start and the size.
func simEntries(seed, big int64) (starts []int64, size int64) {
	r := rand.New(rand.NewSource(seed))
	for i := range 300 {
		starts = append(starts, size)
		n := int64(64<<10 + r.Intn(6<<20))
		if i%60 == 30 {
			n = big
		}
		size += n
	}
	return starts, size
}

// heldBack is how many bytes on disk belong to Entries not yet all there:
// the space a Download as folder can't free yet.
func heldBack(p *plan, starts []int64, size int64) int64 {
	missing := p.ranges()
	var held int64
	for i, s := range starts {
		e := size
		if i+1 < len(starts) {
			e = starts[i+1]
		}
		var gone int64
		for _, r := range missing {
			gone += max(0, min(r.End, e)-max(r.Start, s))
		}
		if gone > 0 {
			held += e - s - gone
		}
	}
	return held
}

// ahead is how many bytes are on disk past the first one missing: what an
// archive read front to back (a solid one, or one big Entry) can't use yet.
func ahead(p *plan, size int64) int64 {
	first := size
	for _, r := range p.ranges() {
		first = min(first, r.Start)
	}
	return p.done() - first
}

// simulate runs connections on three interfaces at different speeds through
// the plan, claiming and fetching as the download loop does, and returns the
// bytes held back once half the archive is on disk and on average over the
// whole download, and the most that was ever on disk ahead of the front.
func simulate(ordered bool, bounds, starts []int64, size int64) (half, avg, most int64) {
	speeds := []float64{12 << 20, 12 << 20, 4 << 20, 4 << 20, 1 << 20, 1 << 20}
	p := newPlan(size, true, []Range{{0, size}})
	p.bounds, p.ordered = bounds, ordered
	ws := make([]*worker, len(speeds))
	segs := make([]*segment, len(speeds))
	for i, sp := range speeds {
		ws[i] = &worker{speed: sp}
	}
	var sum, steps int64
	half = -1
	for p.remaining() > 0 {
		for i, w := range ws {
			if segs[i] == nil {
				if s := p.claim(w.speed); s != nil {
					s.owner, segs[i] = w, s
				}
			}
		}
		for i, w := range ws {
			s := segs[i]
			if s == nil {
				continue
			}
			n := int64(w.speed / 4) // a quarter of a second
			off, k := p.take(s, n)
			p.wrote(s, off, k)
			if k < n || s.written >= s.end {
				p.release(s)
				segs[i] = nil
			}
		}
		p.prune()
		h := heldBack(p, starts, size)
		sum, steps = sum+h, steps+1
		most = max(most, ahead(p, size))
		if half < 0 && p.done() >= size/2 {
			half = h
		}
	}
	return half, sum / steps, most
}

// On several interfaces at different speeds, an ordered plan holds back less
// than the speed-based split: in Entries not yet all there, on average
// through each download, and above all ahead of the front, which is what an
// archive read front to back has to keep on disk. That stays within a few
// strides of the download's speed, whatever the archive's size.
func TestOrderedPlanHoldsBackLess(t *testing.T) {
	const speed = 34 << 20 // of all six connections
	for _, big := range []int64{20 << 20, 300 << 20} {
		for seed := int64(1); seed <= 6; seed++ {
			starts, size := simEntries(seed, big)
			_, ta, tm := simulate(false, nil, starts, size)
			_, oa, om := simulate(true, append(starts[:len(starts):len(starts)], size), starts, size)
			t.Logf("big Entries %d MiB, archive %d (%d MiB): held back on average %d MiB split by speed, %d ordered; most ahead of the front %d, %d MiB",
				big>>20, seed, size>>20, ta>>20, oa>>20, tm>>20, om>>20)
			if oa >= ta {
				t.Errorf("archive %d with %d MiB Entries: ordered held back %d on average, split by speed %d", seed, big>>20, oa, ta)
			}
			if om > 2*stride*speed {
				t.Errorf("archive %d with %d MiB Entries: ordered had %d MiB ahead of the front, more than two strides (%d MiB)", seed, big>>20, om>>20, 2*stride*speed>>20)
			}
		}
	}
}
