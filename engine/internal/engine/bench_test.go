package engine

import (
	"fmt"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/ipc"
)

// benchEngine holds n downloads with no network: paused and finished ones
// are never started (a queued one would be by Schedule, which is not called).
func benchEngine(b *testing.B, n int) *Engine {
	b.Helper()
	e, err := open(b.TempDir(), nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(e.Shutdown)
	dir := b.TempDir()
	for i := range n {
		st := download.State{
			ID: fmt.Sprintf("d%d", i), URL: fmt.Sprintf("https://x.test/f%d.bin", i), Dir: dir,
			Filename: fmt.Sprintf("f%d.bin", i), Size: 1 << 20, Probed: true,
			Status: download.StatusPaused, Added: time.Now(),
		}
		if i%2 == 0 {
			st.Status, st.Finished = download.StatusCompleted, time.Now()
		}
		e.queue.Add(download.New(e, st))
	}
	return e
}

// The 1 s tick builds the state, then encodes it for the watching clients.
func BenchmarkUpdateSnapshot(b *testing.B) {
	for _, n := range []int{500, 2000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			e := benchEngine(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				e.updateSnapshot(time.Second)
			}
		})
	}
}

func BenchmarkEncodeState(b *testing.B) {
	for _, n := range []int{500, 2000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			e := benchEngine(b, n)
			e.updateSnapshot(time.Second)
			snap := e.Snapshot()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := ipc.EncodeEvent("state", snap); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
