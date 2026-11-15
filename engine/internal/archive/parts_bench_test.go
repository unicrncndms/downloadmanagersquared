package archive

import (
	"fmt"
	"testing"
)

func BenchmarkPartOfOrdinaryNames(b *testing.B) {
	names := make([]string, 2000)
	for i := range names {
		names[i] = fmt.Sprintf("movie %d.mkv", i)
	}
	b.ReportAllocs()
	for b.Loop() {
		for _, n := range names {
			PartOf(n)
		}
	}
}
