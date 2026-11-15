package download

import (
	"fmt"
	"testing"
)

func BenchmarkSummaryFinished2000(b *testing.B) {
	dir := b.TempDir()
	ds := make([]*Download, 2000)
	for i := range ds {
		ds[i] = New(&testEnv{}, State{ID: fmt.Sprint(i), URL: "http://example.com/a", Filename: fmt.Sprintf("movie %d.mkv", i), Dir: dir, Status: StatusCompleted, Size: 10, Probed: true, RangeOK: true})
	}
	b.ReportAllocs()
	for b.Loop() {
		for _, d := range ds {
			d.Summary()
		}
	}
}
