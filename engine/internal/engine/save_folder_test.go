package engine

import (
	"fmt"
	"testing"
	"time"
)

// Add takes the folder to save into, an absolute one; setDir moves a paused
// download's save folder (the move itself is download.SetDir's test).
func TestAddAndSetDirTakeAFolderChosen(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	dir, other := t.TempDir(), t.TempDir()
	if _, err := e.Add(AddRequest{URL: "https://example.com/a.iso", Dir: "relative"}); err == nil {
		t.Fatal("Add took a folder that isn't one")
	}
	id, err := e.Add(AddRequest{URL: "https://example.com/a.iso", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if s := summaryOf(t, e, id); s.Dir != dir {
		t.Fatalf("saves to %q, want %q", s.Dir, dir)
	}
	tid, err := e.Add(AddRequest{URL: testMagnet, VPN: "off", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if s := summaryOf(t, e, tid); s.Dir != dir {
		t.Fatalf("the torrent saves to %q, want %q", s.Dir, dir)
	}
	if _, err := e.Call(nil, "chooseFiles", []byte(fmt.Sprintf(`{"id":%q,"files":[true],"dir":"relative"}`, tid))); err == nil {
		t.Fatal("chooseFiles took a folder that isn't one")
	}
	if _, err := e.Call(nil, "pause", []byte(fmt.Sprintf(`{"id":%q}`, id))); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Call(nil, "setDir", []byte(fmt.Sprintf(`{"id":%q,"dir":%q}`, id, other))); err != nil {
		t.Fatal(err)
	}
	if s := summaryOf(t, e, id); s.Dir != other {
		t.Fatalf("after setDir saves to %q, want %q", s.Dir, other)
	}
	if _, err := e.Call(nil, "setDir", []byte(fmt.Sprintf(`{"id":%q,"dir":"relative"}`, id))); err == nil {
		t.Fatal("setDir took a folder that isn't one")
	}
}

// pickSaveFolder shows the picker at the current download folder and saves
// the folder chosen; cancelled, nothing changes.
func TestPickSaveFolderSetsTheDownloadFolder(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	chosen := t.TempDir()
	pick := chosen
	got := make(chan string, 2)
	e.pickFolder = func(owner uintptr, start, title string) (string, bool) {
		got <- start
		return pick, pick != ""
	}
	e.mu.Lock()
	before := e.settings.SaveDir
	e.mu.Unlock()
	if _, err := e.Call(nil, "pickSaveFolder", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if start := <-got; start != before {
		t.Fatalf("picker started at %q, want %q", start, before)
	}
	waitFor(t, "the chosen folder", 5*time.Second, func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.settings.SaveDir == chosen
	})
	pick = ""
	e.Call(nil, "pickSaveFolder", []byte("{}"))
	<-got
	time.Sleep(50 * time.Millisecond)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.settings.SaveDir != chosen {
		t.Fatalf("a cancelled picker changed the folder to %q", e.settings.SaveDir)
	}
}
