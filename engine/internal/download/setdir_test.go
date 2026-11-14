package download

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A paused download's partial file moves with it into the new folder, and
// resuming finishes the file there.
func TestAPausedDownloadResumesInTheNewFolder(t *testing.T) {
	data := buildZip(t, manyFiles("", 40, 256<<10))
	srv := newZipServer(data)
	defer srv.Close()
	root := t.TempDir()
	from, to := filepath.Join(root, "a"), filepath.Join(root, "b")
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 5*time.Millisecond)}, main: "eth"}
	d := startUntil(t, env, State{ID: "sd1", URL: srv.URL + "/dl", Dir: from, Size: -1}, func(d *Download) bool {
		s := d.Summary()
		return s.Done > 0 && s.Status == StatusRunning
	})
	d.Stop(StatusPaused)
	if err := d.SetDir("relative"); err == nil {
		t.Fatal("took a folder that isn't one")
	}
	if err := d.SetDir(to); err != nil {
		t.Fatal(err)
	}
	if s := d.Summary(); s.Dir != to {
		t.Fatalf("dir %q", s.Dir)
	}
	if left := names(t, from); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
	if got := names(t, to); len(got) != 1 {
		t.Fatalf("in the new folder: %v", got)
	}
	st := d.State()
	st.Status = StatusQueued
	d.dropParked() // d2 runs in its place, as after an engine restart
	d2 := runToEnd(t, env, st)
	if s := d2.Summary(); s.Status != StatusCompleted || s.Dir != to {
		t.Fatalf("status %s dir %s: %s", s.Status, s.Dir, s.Error)
	}
	checkFile(t, filepath.Join(to, d2.State().Filename), data)
	if _, err := os.Stat(from); err == nil {
		if left := names(t, from); len(left) != 0 {
			t.Fatalf("left behind: %v", left)
		}
	}
	if err := d2.SetDir(from); err == nil {
		t.Fatal("moved a complete download")
	}
}
