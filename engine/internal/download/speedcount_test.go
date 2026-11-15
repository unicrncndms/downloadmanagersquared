package download

import (
	"io"
	"sync/atomic"
	"testing"
	"time"
)

type slowReader struct{}

// 10 KB every 50 ms: 200 KB/s.
func (slowReader) Read(p []byte) (int, error) {
	time.Sleep(50 * time.Millisecond)
	return min(len(p), 10_000), nil
}

// On a slow line the speed counters move with every read, not once per 256 KiB
// buffer, so each 500 ms tick sees about its share.
func TestSpeedIsCountedPerRead(t *testing.T) {
	var w, ir atomic.Int64
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	r := stallReader{slowReader{}, timer, [2]*atomic.Int64{&w, &ir}}
	go func() {
		for { // buffers, as fetch reads them
			if _, err := io.ReadFull(r, make([]byte, bufSize)); err != nil {
				return
			}
		}
	}()
	var last int64
	for i := range 6 {
		time.Sleep(500 * time.Millisecond)
		now := w.Load()
		if d := now - last; d < 60_000 || d > 140_000 {
			t.Errorf("tick %d saw %d bytes, want about 100000", i, d)
		}
		last = now
	}
	if ir.Load() != w.Load() {
		t.Errorf("worker counted %d, interface %d", w.Load(), ir.Load())
	}
}
