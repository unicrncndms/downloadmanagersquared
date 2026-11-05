package download

import (
	"math/rand"
	"slices"
	"testing"
)

// simWorker is one connection in the simulation: what it has taken from its
// chunk and not yet written.
type simWorker struct {
	w        *worker
	seg      *segment
	off, n   int64
	inFlight bool
}

// coverage records every byte range written to the file.
type coverage struct {
	t      *testing.T
	writes []Range
}

func (c *coverage) add(off, n int64) {
	c.t.Helper()
	r := Range{off, off + n}
	for _, w := range c.writes {
		if r.Start < w.End && w.Start < r.End {
			c.t.Fatalf("bytes %d-%d written twice (already %d-%d)", r.Start, r.End, w.Start, w.End)
		}
	}
	c.writes = append(c.writes, r)
}

// complete: the writes cover exactly [0,size), with nothing missing.
func (c *coverage) complete(size int64) {
	c.t.Helper()
	ws := slices.Clone(c.writes)
	slices.SortFunc(ws, func(a, b Range) int { return int(a.Start - b.Start) })
	var at int64
	for _, w := range ws {
		if w.Start != at {
			c.t.Fatalf("bytes %d-%d never written", at, w.Start)
		}
		at = w.End
	}
	if at != size {
		c.t.Fatalf("bytes %d-%d never written", at, size)
	}
}

// Connections of very different speeds take over each other's chunks, fail,
// and the download is paused and resumed from its saved ranges, in random
// orders. Through all of it no byte is written twice, and every byte is
// written in the end.
func TestTakeoverNeverWritesTwiceOrSkips(t *testing.T) {
	for seed := int64(1); seed <= 200; seed++ {
		rng := rand.New(rand.NewSource(seed))
		size := int64(32<<20 + rng.Intn(64<<20))
		cov := &coverage{t: t}
		p := newPlan(size, true, []Range{{0, size}})
		var sims []*simWorker
		for i := 0; i < 2+rng.Intn(6); i++ {
			sims = append(sims, &simWorker{w: &worker{speed: float64(1+rng.Intn(100)) * 1e5}})
		}

		for step := 0; step < 1_000_000 && p.remaining() > 0; step++ {
			s := sims[rng.Intn(len(sims))]
			switch {
			case s.inFlight:
				// The write lands (outside the lock, so other connections may
				// have split this chunk meanwhile).
				cov.add(s.off, s.n)
				p.wrote(s.seg, s.off, s.n)
				s.inFlight = false
			case s.seg == nil:
				if s.seg = p.claim(s.w.speed); s.seg != nil {
					s.seg.owner = s.w
				}
			case rng.Intn(50) == 0:
				// The connection fails or is closed.
				p.release(s.seg)
				s.seg = nil
			case s.seg.pos >= s.seg.end:
				p.release(s.seg) // chunk finished
				s.seg = nil
			default:
				// Data arrives: a read of up to a buffer, faster connections reading more often.
				if rng.Float64()*100e5 > s.w.speed {
					continue
				}
				s.off, s.n = p.take(s.seg, int64(1+rng.Intn(bufSize)))
				s.inFlight = s.n > 0
			}

			if rng.Intn(20000) == 0 {
				// Pause: writes in flight land, every chunk is let go, and the
				// download comes back from the saved ranges, as after a restart.
				for _, s := range sims {
					if s.inFlight {
						cov.add(s.off, s.n)
						p.wrote(s.seg, s.off, s.n)
						s.inFlight = false
					}
					if s.seg != nil {
						p.release(s.seg)
						s.seg = nil
					}
				}
				p = newPlan(size, true, p.ranges())
			}
		}
		if p.remaining() > 0 {
			t.Fatalf("seed %d: %d bytes still missing after the simulation", seed, p.remaining())
		}
		cov.complete(size)
	}
}
