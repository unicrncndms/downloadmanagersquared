package torrent

import (
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/download"
)

func TestUploadedAloneMarksTheEntryDirtyOnlyEvery30Seconds(t *testing.T) {
	clock := time.Now()
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = time.Now })
	dirty := 0
	tr := &Torrent{s: &Session{cfg: Config{Changed: func(*Torrent) { dirty++ }}}}
	step := func(d time.Duration, f func()) int {
		clock = clock.Add(d)
		dirty = 0
		tr.update(nil, f)
		return dirty
	}
	up := int64(0)
	bump := func() { up++; tr.st.Uploaded = up }

	if step(time.Minute, bump) != 1 {
		t.Fatal("first upload change after a long while must be saved")
	}
	if step(10*time.Second, bump) != 0 || step(10*time.Second, func() {}) != 0 {
		t.Fatal("upload-only change within 30 s must not mark dirty")
	}
	if step(11*time.Second, bump) != 1 {
		t.Fatal("upload change after 30 s must mark dirty")
	}
	if step(time.Second, func() { tr.st.Status = download.StatusSeeding }) != 1 {
		t.Fatal("a Status change must mark dirty at once")
	}
}
