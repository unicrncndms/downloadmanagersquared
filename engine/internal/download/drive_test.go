package download

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDrivePauseNamesTheDriveAndWhatToDo(t *testing.T) {
	full := &fs.PathError{Op: "write", Path: `E:\dl\a.dms.part`, Err: errDiskFull}
	if pe := drivePause(fmt.Errorf("x: %w", full), `E:\dl`, 3e9); pe == nil || pe.msg != "Paused: drive E: is full (3.0 GB still to download): free some space, then resume" || pe.drive != "" {
		t.Errorf("disk full: %+v", pe)
	}
	gone := &fs.PathError{Op: "write", Path: `F:\a.dms.part`, Err: errNotReady}
	if pe := drivePause(gone, `F:\`, 0); pe == nil || pe.drive != "F:" || !strings.Contains(pe.msg, "drive F: is not available. It resumes by itself") {
		t.Errorf("drive gone: %+v", pe)
	}
	if pe := drivePause(&fs.PathError{Op: "open", Path: `\\nas\share\a`, Err: errBadNetPath}, `\\nas\share\dl`, 0); pe == nil || !strings.Contains(pe.msg, `the network folder \\nas\share`) {
		t.Errorf("share gone: %+v", pe)
	}
	if pe := drivePause(&fs.PathError{Op: "write", Path: `C:\a`, Err: errAccessDenied}, `C:\`, 0); pe != nil {
		t.Errorf("access denied is no drive trouble: %+v", pe)
	}
}

// A drive pulled out mid-download pauses the download, saying which drive,
// and it resumes by itself once the drive is back. Nothing is failed or lost.
func TestADriveThatGoesPausesAndResumes(t *testing.T) {
	data := randomData(32 << 20)
	srv := fileServer(t, data, nil)
	defer srv.Close()
	dir := t.TempDir()
	drive := dir[:2]
	there := true
	was := driveThere
	driveThere = func(d string) bool { return d != drive || there }
	defer func() { driveThere = was }()
	env := &testEnv{ifaces: []Iface{iface("any", 2, 5*time.Millisecond)}, main: "any", finished: make(chan struct{})}
	d := New(env, State{ID: "dg", URL: srv.URL + "/file/any", Dir: dir, Size: -1})
	d.Start()
	waitSummary(t, d, func(s Summary) bool { return s.Done > 0 }, "some bytes")
	there = false
	d.mu.Lock()
	d.file.Close() // every write from here on fails, as on a drive that is gone
	d.mu.Unlock()
	waitSummary(t, d, func(s Summary) bool { return s.Status == StatusPaused }, "a pause")
	s := Present(d.Summary())
	if !strings.Contains(s.Error, "drive "+drive+" is not available") || d.State().WaitDrive != drive || s.Section != SectionPaused {
		t.Fatalf("paused with %q, waits for %q, section %s", s.Error, d.State().WaitDrive, s.Section)
	}
	if d.DriveBack() {
		t.Fatal("back while the drive is still gone")
	}
	there = true
	d.mu.Lock()
	d.driveChecked = time.Time{}
	d.mu.Unlock()
	if !d.DriveBack() {
		t.Fatal("the drive came back, but the download doesn't see it")
	}
}

// Saved progress never runs ahead of the disk: bytes that arrived since the
// last flush aren't counted done in the state, so a power loss can't leave
// the state saying so of bytes only Windows' cache had.
func TestSavedProgressCountsOnlyFlushedBytes(t *testing.T) {
	data := randomData(32 << 20)
	srv := fileServer(t, data, nil)
	defer srv.Close()
	env := &testEnv{ifaces: []Iface{iface("any", 2, 5*time.Millisecond)}, main: "any", finished: make(chan struct{})}
	d := New(env, State{ID: "fl", URL: srv.URL + "/file/any", Dir: t.TempDir(), Size: -1})
	d.Start()
	defer d.Stop(StatusPaused)
	waitSummary(t, d, func(s Summary) bool { return s.Done > 0 }, "some bytes")
	left := func() (n int64) {
		for _, r := range d.State().Remaining {
			n += r.End - r.Start
		}
		return n
	}
	if n := left(); n != int64(len(data)) {
		t.Fatalf("before a flush the state already counts %d bytes done", int64(len(data))-n)
	}
	d.flush()
	if n := left(); n >= int64(len(data)) {
		t.Fatal("after a flush the state still counts nothing done")
	}
}

// A paused download whose partial file was deleted starts over when resumed,
// instead of failing on a file that isn't there.
func TestADeletedPartStartsOver(t *testing.T) {
	data := randomData(16 << 20)
	srv := fileServer(t, data, nil)
	defer srv.Close()
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("any", 2, 5*time.Millisecond)}, main: "any", finished: make(chan struct{})}
	d := New(env, State{ID: "dp", URL: srv.URL + "/file/any", Dir: dir, Size: -1})
	d.Start()
	waitSummary(t, d, func(s Summary) bool { return s.Done > 0 }, "some bytes")
	d.Stop(StatusPaused)
	for d.Running() {
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.Remove(d.partPath()); err != nil {
		t.Fatal(err)
	}
	d.Start()
	select {
	case <-env.finished:
	case <-time.After(60 * time.Second):
		t.Fatalf("did not finish: %+v", d.Summary())
	}
	if s := d.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkFile(t, filepath.Join(dir, d.State().Filename), data)
}
