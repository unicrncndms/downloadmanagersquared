package archive

import (
	"testing"
	"time"
)

// countFile counts Free calls, one syscall each on a real file.
type countFile struct {
	File
	frees int
}

func (c *countFile) Free(off, n int64) error { c.frees++; return nil }

// 100k Entries of 1 KB, each reaching into its done neighbours, are freed with
// one call per contiguous run, and Freeing sees merged ranges too.
func TestFreeAllMakesOneCallPerRun(t *testing.T) {
	const n, each = 100_000, 1 << 10
	var rs []Range
	for i := range n {
		s := int64(i) * each
		rs = append(rs, Range{max(s-freeReach, 0), min(s+each+freeReach, n*each)})
	}
	f := &countFile{}
	var told int
	x := &Extractor{f: f, Freeing: func(rs []Range) error { told = len(rs); return nil }}
	start := time.Now()
	if err := x.freeAll(rs); err != nil {
		t.Fatal(err)
	}
	t.Logf("freeAll of %d ranges: %v", n, time.Since(start))
	if f.frees != 1 || told != 1 {
		t.Fatalf("%d Free calls, Freeing told %d ranges; want 1 and 1", f.frees, told)
	}
}
