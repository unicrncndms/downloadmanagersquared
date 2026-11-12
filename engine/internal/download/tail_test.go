package download

import (
	"testing"
)

// finishTime plays a download out over connections of the given speeds (bytes/s),
// as the engine runs it: each takes a chunk from the plan, fetches it, and
// takes the next, or the far end of a slower one's. A new request needs setup
// seconds before bytes flow. It returns when the last byte is on disk.
func finishTime(size int64, speeds []float64, setup float64) float64 {
	const dt = 0.01
	p := newPlan(size, true, []Range{{0, size}})
	type conn struct {
		w     *worker
		seg   *segment
		delay float64
	}
	cs := make([]*conn, len(speeds))
	var sum float64
	for _, v := range speeds {
		sum += v
	}
	start := func(c *conn) {
		// What the engine knows of a connection: how fast it has been.
		if c.seg = p.claim(c.w.speed); c.seg != nil {
			c.seg.owner, c.delay = c.w, setup
		}
	}
	for i, v := range speeds {
		cs[i] = &conn{w: &worker{iface: "x", speed: v}}
		start(cs[i])
	}
	for t := 0.0; ; t += dt {
		if p.remaining() == 0 {
			return t
		}
		if t > 3600 {
			panic("never finishes")
		}
		for _, c := range cs {
			if c.seg == nil {
				// Idle: try again, as the engine does when a tick finds work.
				if start(c); c.seg == nil {
					continue
				}
			}
			if c.delay > 0 {
				c.delay -= dt
				continue
			}
			off, k := p.take(c.seg, int64(c.w.speed*dt)+1)
			p.wrote(c.seg, off, k)
			if c.seg.written >= c.seg.end {
				p.release(c.seg)
				c.seg = nil
				start(c)
			}
		}
	}
}

// The end of a download waits for the slowest connection: a slow connection
// that holds a few MiB (too small to split) keeps the fast ones idle. The
// download should finish close to size over the sum of the speeds.
func TestTheEndDoesNotWaitForTheSlowestConnection(t *testing.T) {
	const MB = 1e6
	// Four interfaces of four connections each, as in a real run: 0.7, 1.7, 1.6 and 3.5 MB/s each.
	var speeds []float64
	for _, v := range []float64{0.7, 1.7, 1.6, 3.5} {
		for range 4 {
			speeds = append(speeds, v*MB)
		}
	}
	var sum float64
	for _, v := range speeds {
		sum += v
	}
	for _, size := range []int64{200 << 20, 512 << 20, 1 << 30} {
		ideal := float64(size) / sum
		got := finishTime(size, speeds, 0.1)
		t.Logf("%d MiB: %.1f s, ideal %.1f s, the end costs %.1f s", size>>20, got, ideal, got-ideal)
		if got-ideal > 3 {
			t.Errorf("%d MiB took %.1f s, %.1f s more than the %.1f s the speeds add up to", size>>20, got, got-ideal, ideal)
		}
	}
}

// A slow start for each new request (a tether with a long round trip) must not
// make taking over small pieces cost more than it saves.
func TestTakingOverSmallPiecesPaysOffOnASlowStart(t *testing.T) {
	const MB = 1e6
	speeds := []float64{0.7 * MB, 0.7 * MB, 1.7 * MB, 1.7 * MB, 3.5 * MB, 3.5 * MB, 3.5 * MB, 3.5 * MB}
	var sum float64
	for _, v := range speeds {
		sum += v
	}
	size := int64(512 << 20)
	ideal := float64(size) / sum
	got := finishTime(size, speeds, 0.5)
	t.Logf("slow start: %.1f s, ideal %.1f s", got, ideal)
	if got-ideal > 5 {
		t.Errorf("took %.1f s, %.1f s more than the %.1f s the speeds add up to", got, got-ideal, ideal)
	}
}
