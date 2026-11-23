package torrent

import (
	"testing"
	"time"
)

// A torrent with some of its files chosen shows their size, and its ratio is
// what it uploaded over what it downloaded, as other clients count it: not
// over the whole torrent, which made a small ratio read 0.00.
func TestRatioAndSizeAreOfTheChosenFiles(t *testing.T) {
	tr := &Torrent{st: State{ID: "x", Size: 200 << 30, Uploaded: 1 << 29, Finished: time.Now()}, wantSize: 2 << 30}
	s := tr.Summary()
	if s.Size != 2<<30 || s.Done != 2<<30 {
		t.Fatalf("size %d, done %d, want both 2 GiB", s.Size, s.Done)
	}
	if s.Ratio != 0.25 {
		t.Fatalf("ratio %v, want 0.25", s.Ratio)
	}
	tr.wantSize = 0 // files not known yet: the whole torrent
	if s := tr.Summary(); s.Size != 200<<30 {
		t.Fatalf("size before the files are known %d", s.Size)
	}
}
