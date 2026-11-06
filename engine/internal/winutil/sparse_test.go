package winutil

import (
	"os"
	"path/filepath"
	"testing"
)

func sparseFile(t *testing.T, size int64) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "sparse"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	if err := SetSparse(f, true); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	return f
}

func allocated(t *testing.T, f *os.File) int64 {
	t.Helper()
	rs, err := AllocatedRanges(f)
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	for _, r := range rs {
		n += r.End - r.Start
	}
	return n
}

func TestASparseFileHasOnlyWrittenBytesAllocated(t *testing.T) {
	f := sparseFile(t, 64<<20)
	if n := allocated(t, f); n != 0 {
		t.Fatalf("a new sparse file has %d bytes allocated", n)
	}
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = 1
	}
	if _, err := f.WriteAt(data, 8<<20); err != nil {
		t.Fatal(err)
	}
	rs, err := AllocatedRanges(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || rs[0].Start > 8<<20 || rs[0].End < 9<<20 || rs[0].End-rs[0].Start > 2<<20 {
		t.Fatalf("allocated %v after writing 8-9 MB", rs)
	}
}

func TestFreeRangeGivesTheSpaceBackAndReadsAsZeros(t *testing.T) {
	f := sparseFile(t, 16<<20)
	data := make([]byte, 4<<20)
	for i := range data {
		data[i] = 0xab
	}
	if _, err := f.WriteAt(data, 4<<20); err != nil {
		t.Fatal(err)
	}
	if err := FreeRange(f, 4<<20, 4<<20); err != nil {
		t.Fatal(err)
	}
	if n := allocated(t, f); n != 0 {
		t.Fatalf("%d bytes still allocated after freeing all that was written", n)
	}
	got := make([]byte, 4<<20)
	if _, err := f.ReadAt(got, 4<<20); err != nil {
		t.Fatal(err)
	}
	for i, b := range got {
		if b != 0 {
			t.Fatalf("byte %d reads %#x after freeing", i, b)
		}
	}
	if fi, _ := f.Stat(); fi.Size() != 16<<20 {
		t.Fatalf("freeing changed the size to %d", fi.Size())
	}
}

func TestFreeRangeLeavesTheBytesAroundIt(t *testing.T) {
	f := sparseFile(t, 4<<20)
	data := make([]byte, 3<<20)
	for i := range data {
		data[i] = 7
	}
	if _, err := f.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	if err := FreeRange(f, 1<<20, 1<<20); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 1)
	for _, off := range []int64{0, 1<<20 - 1, 2 << 20, 3<<20 - 1} {
		if _, err := f.ReadAt(b, off); err != nil || b[0] != 7 {
			t.Fatalf("byte %d is %d (%v) after freeing 1-2 MB", off, b[0], err)
		}
	}
	if n := allocated(t, f); n < 2<<20 || n > 2<<20+128<<10 {
		t.Fatalf("%d bytes allocated, want about 2 MB", n)
	}
}

func TestAllocatedRangesOfManyPieces(t *testing.T) {
	f := sparseFile(t, 256<<20)
	b := []byte{1}
	// More pieces than one call's buffer holds.
	for i := int64(0); i < 256; i++ {
		if _, err := f.WriteAt(b, i<<20); err != nil {
			t.Fatal(err)
		}
	}
	rs, err := AllocatedRanges(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 256 {
		t.Fatalf("%d ranges, want 256", len(rs))
	}
	for i := 1; i < len(rs); i++ {
		if rs[i].Start < rs[i-1].End {
			t.Fatalf("ranges out of order: %v then %v", rs[i-1], rs[i])
		}
	}
}
