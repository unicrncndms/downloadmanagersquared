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

// simulate runs connections on three interfaces at different speeds through
// the plan, claiming and fetching as the download loop does, and returns the
// bytes held back once half the archive is on disk and on average over the
// whole download.
func simulate(bounds, starts []int64, size int64) (half, avg int64) {
	speeds := []float64{12 << 20, 12 << 20, 4 << 20, 4 << 20, 1 << 20, 1 << 20}
	p := newPlan(size, true, []Range{{0, size}})
	p.bounds = bounds
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
		if half < 0 && p.done() >= size/2 {
			half = h
		}
	}
	return half, sum / steps
}

// On several interfaces at different speeds, sending connections to Entry
// fronts and cutting on boundaries holds back less than today's scheduler:
// at the halfway point over several archives, and on average through each
// download. (One snapshot of one archive with a few huge Entries can go
// either way: it depends on which huge Entry is part-way through then.)
func TestEntryAwareSchedulingHoldsBackLess(t *testing.T) {
	for _, big := range []int64{20 << 20, 300 << 20} {
		var todayHalf, awareHalf int64
		for seed := int64(1); seed <= 6; seed++ {
			starts, size := simEntries(seed, big)
			th, ta := simulate(nil, starts, size)
			ah, aa := simulate(append(starts[:len(starts):len(starts)], size), starts, size)
			todayHalf, awareHalf = todayHalf+th, awareHalf+ah
			t.Logf("big Entries %d MiB, archive %d: halfway today %d MiB, Entry-aware %d MiB; average %d, %d MiB",
				big>>20, seed, th>>20, ah>>20, ta>>20, aa>>20)
			if aa >= ta {
				t.Errorf("archive %d with %d MiB Entries: Entry-aware held back %d on average, today's %d", seed, big>>20, aa, ta)
			}
		}
		if awareHalf >= todayHalf {
			t.Errorf("with %d MiB Entries: Entry-aware held back %d halfway in all, today's %d", big>>20, awareHalf, todayHalf)
		}
	}
}
