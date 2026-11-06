package archive

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// lowerLarge makes Entries over n bytes large, freed every step bytes.
func lowerLarge(t *testing.T, n, step int64) {
	t.Helper()
	was, wasStep := LargeEntry, FreeStep
	LargeEntry, FreeStep = n, step
	t.Cleanup(func() { LargeEntry, FreeStep = was, wasStep })
}

func sumRanges(rs []Range) int64 {
	var n int64
	for _, r := range rs {
		n += r.End - r.Start
	}
	return n
}

// A large Entry is extracted as its bytes arrive from its front, and what the
// decompressor has passed is freed, so the .part never holds much of it.
func TestALargeEntryIsExtractedInOrderAndFreedBehind(t *testing.T) {
	for _, store := range []bool{true, false} {
		lowerLarge(t, 1<<20, 1<<20)
		big := random(16 << 20)
		data := goZip(t, "",
			goFile{name: "small.txt", data: []byte("before")},
			goFile{name: "big.bin", data: big, store: store},
			goFile{name: "after.txt", data: []byte("after")},
		)
		d := newDownload(t, data)
		a := d.tail()
		e := a.Entries[1]
		out := t.TempDir()
		x := extractor(t, a, d.f, out)
		defer x.Close()

		var freed []Range
		worst := int64(0)
		for off := int64(0); off < a.DirStart; off += 512 << 10 {
			d.arrive(off, min(off+512<<10, a.DirStart))
			r := extract(t, x, d.missing)
			if len(r.Failed) > 0 {
				t.Fatalf("failed: %v", r.Failed[0])
			}
			freed = append(freed, r.Freed...)
			worst = max(worst, d.allocatedIn(Range{e.Start, e.End}))
		}
		if worst > 4<<20 {
			t.Fatalf("store %v: up to %d of the Entry's %d bytes allocated at once", store, worst, e.End-e.Start)
		}
		if !x.Finished() {
			t.Fatalf("store %v: not finished", store)
		}
		got, err := os.ReadFile(filepath.Join(out, "big.bin"))
		if err != nil || !bytes.Equal(got, big) {
			t.Fatalf("store %v: big.bin wrong (%v)", store, err)
		}
		if l := leftovers(t, out); len(l) > 0 {
			t.Fatalf("left %v", l)
		}
		if n := sumRanges(merge(freed)); n != a.DirStart {
			t.Fatalf("freed %d bytes, the Entries take %d", n, a.DirStart)
		}
	}
}

// Freed bytes are reported as they are freed, one step per call, before the
// Entry is done, so the caller can save them before more are freed.
func TestALargeEntrysFreedBytesAreReportedBeforeItIsDone(t *testing.T) {
	lowerLarge(t, 1<<20, 1<<20)
	data := goZip(t, "", goFile{name: "big.bin", data: random(8 << 20), store: true})
	d := newDownload(t, data)
	a := d.tail()
	e := a.Entries[0]
	x := extractor(t, a, d.f, t.TempDir())
	defer x.Close()
	d.arrive(0, e.Start+4<<20)
	one, err := x.Extract(context.Background(), d.missing)
	if err != nil {
		t.Fatal(err)
	}
	if !one.More || len(one.Freed) != 1 || sumRanges(one.Freed) > 2<<20 {
		t.Fatalf("one call: more %v, freed %v; want one step and more to come", one.More, one.Freed)
	}
	r := extract(t, x, d.missing)
	r.Freed = append(one.Freed, r.Freed...)
	if last, err := x.Extract(context.Background(), d.missing); err != nil || last.More || len(last.Freed) != 0 {
		t.Fatalf("with nothing more on disk: %+v, %v", last, err)
	}
	if len(r.Done) != 0 {
		t.Fatalf("done %v with half its bytes", r.Done)
	}
	n := sumRanges(r.Freed)
	if n < 2<<20 || n > 4<<20 {
		t.Fatalf("freed %d bytes of the 4 MB passed", n)
	}
	if r.Freed[0].Start != e.Start {
		t.Fatalf("freed from %d, the Entry starts at %d", r.Freed[0].Start, e.Start)
	}
}

// Stopped partway, a large Entry leaves no unfinished file, and a new
// Extractor extracts it from its start once its freed bytes are back.
func TestAStoppedLargeEntryStartsOverFromItsFront(t *testing.T) {
	lowerLarge(t, 1<<20, 1<<20)
	big := random(8 << 20)
	data := goZip(t, "", goFile{name: "big.bin", data: big})
	d := newDownload(t, data)
	a := d.tail()
	e := a.Entries[0]
	out := t.TempDir()
	x := extractor(t, a, d.f, out)
	d.arrive(0, e.Start+5<<20)
	r := extract(t, x, d.missing)
	if len(r.Freed) == 0 {
		t.Fatal("nothing freed")
	}
	x.Close()
	if l := leftovers(t, out); len(l) > 0 {
		t.Fatalf("left %v after Close", l)
	}

	// After a crash: the unfinished file is still there, the freed bytes are
	// fetched again, and the rest arrives.
	if err := os.WriteFile(filepath.Join(out, "big.bin.dms.part"), []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	x = extractor(t, a, d.f, out)
	defer x.Close()
	if l := leftovers(t, out); len(l) > 0 {
		t.Fatalf("left %v after a new Extractor", l)
	}
	for _, fr := range r.Freed {
		d.missing = append(d.missing, fr) // back in the plan
	}
	d.arrive(0, a.DirStart)
	r = extract(t, x, d.missing)
	if len(r.Done) != 1 || len(r.Failed) != 0 {
		t.Fatalf("done %v, failed %v", r.Done, r.Failed)
	}
	got, _ := os.ReadFile(filepath.Join(out, "big.bin"))
	if !bytes.Equal(got, big) {
		t.Fatal("big.bin wrong")
	}
}

// A wrong byte in a large Entry fails it as a whole: its whole Range is to be
// fetched again, and nothing of it is kept.
func TestAWrongByteFailsALargeEntryWhole(t *testing.T) {
	for _, store := range []bool{true, false} {
		lowerLarge(t, 1<<20, 1<<20)
		data := goZip(t, "", goFile{name: "big.bin", data: random(6 << 20), store: store})
		d := newDownload(t, data)
		a := d.tail()
		e := a.Entries[0]
		data[e.Start+3<<20] ^= 0xff
		out := t.TempDir()
		x := extractor(t, a, d.f, out)
		defer x.Close()
		var failed []*Failure
		for off := int64(0); off < a.DirStart && len(failed) == 0; off += 1 << 20 {
			d.arrive(off, min(off+1<<20, a.DirStart))
			failed = extract(t, x, d.missing).Failed
		}
		if len(failed) != 1 || failed[0].Range != (Range{e.Start, e.End}) {
			t.Fatalf("store %v: failed %v, want the whole Entry", store, failed)
		}
		if l := leftovers(t, out); len(l) > 0 {
			t.Fatalf("left %v", l)
		}
		if _, err := os.Stat(filepath.Join(out, "big.bin")); err == nil {
			t.Fatal("a wrong Entry was kept")
		}
	}
}

// A cancelled call leaves the large Entry where it was: the next call goes on.
func TestACancelledCallKeepsTheLargeEntryGoing(t *testing.T) {
	lowerLarge(t, 1<<20, 1<<20)
	big := random(4 << 20)
	data := goZip(t, "", goFile{name: "big.bin", data: big})
	d := newDownload(t, data)
	a := d.tail()
	out := t.TempDir()
	x := extractor(t, a, d.f, out)
	defer x.Close()
	d.arrive(0, a.Entries[0].Start+2<<20)
	extract(t, x, d.missing)
	d.arrive(0, a.DirStart)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := x.Extract(ctx, d.missing); !errors.Is(err, context.Canceled) {
		t.Fatalf("Extract: %v", err)
	}
	r := extract(t, x, d.missing)
	if len(r.Done) != 1 {
		t.Fatalf("done %v", r.Done)
	}
	got, _ := os.ReadFile(filepath.Join(out, "big.bin"))
	if !bytes.Equal(got, big) {
		t.Fatal("big.bin wrong")
	}
}
