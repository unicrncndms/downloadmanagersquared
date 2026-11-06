package engine

import (
	"fmt"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/window"
)

// The real Downloads window says which window it is, so the picker opens over it.
var _ windowHandle = (*window.Window)(nil)

// Add's folder switch: true asks for the folder, false leaves it a file even
// with the default on, and leaving it out takes the default.
func TestAddCanSwitchDownloadAsFolderOnOrOff(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	folder := func(id string) *download.Folder {
		d, err := e.get(id)
		if err != nil {
			t.Fatal(err)
		}
		return d.State().Folder
	}
	yes, no := true, false
	on, _ := e.Add(AddRequest{URL: "https://example.com/a.zip", Folder: &yes})
	if f := folder(on); f == nil || !f.On || f.Into != "" {
		t.Fatalf("switched on when added: %+v, want on and waiting for the folder", f)
	}
	e.applySettings(func(s *Settings) { s.FolderDefault = true })
	off, _ := e.Add(AddRequest{URL: "https://example.com/b.zip", Folder: &no})
	if f := folder(off); f != nil {
		t.Fatalf("switched off when added, default on: %+v", f)
	}
}

// pickFolder shows the folder picker over the Downloads window and, when a
// folder is chosen, makes it the download's folder to extract into. It
// answers at once: the picker may stay open longer than a call may take.
func TestPickFolderSetsTheFolderToExtractInto(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	e.SetWindow(handleWindow(42))
	chosen := t.TempDir()
	type shown struct {
		owner        uintptr
		start, title string
	}
	got := make(chan shown, 2)
	pick := chosen
	e.pickFolder = func(owner uintptr, start, title string) (string, bool) {
		got <- shown{owner, start, title}
		return pick, pick != ""
	}
	yes := true
	id, _ := e.Add(AddRequest{URL: "https://example.com/big.zip", Folder: &yes})
	e.mu.Lock()
	start := e.settings.folderDir() // before: the choice becomes the last folder
	e.mu.Unlock()
	if _, err := e.Call(nil, "pickFolder", []byte(fmt.Sprintf(`{"id":%q}`, id))); err != nil {
		t.Fatal(err)
	}
	s := <-got
	if s.owner != 42 || s.start != start || s.title == "" {
		t.Fatalf("picker shown as %+v, want over the window, at the last folder %q", s, start)
	}
	waitFor(t, "the chosen folder", 5*time.Second, func() bool {
		d, _ := e.get(id)
		f := d.State().Folder
		return f != nil && f.Into == chosen
	})
	e.mu.Lock()
	last := e.settings.FolderDir
	e.mu.Unlock()
	if last != chosen {
		t.Fatalf("last folder %q, want %q", last, chosen)
	}

	// Cancelled: nothing changes.
	pick = ""
	other, _ := e.Add(AddRequest{URL: "https://example.com/other.zip", Folder: &yes})
	if _, err := e.Call(nil, "pickFolder", []byte(fmt.Sprintf(`{"id":%q}`, other))); err != nil {
		t.Fatal(err)
	}
	<-got
	time.Sleep(50 * time.Millisecond)
	if d, _ := e.get(other); d.State().Folder.Into != "" {
		t.Fatalf("a cancelled picker set the folder: %+v", d.State().Folder)
	}
	if _, err := e.Call(nil, "pickFolder", []byte(`{"id":"nope"}`)); err == nil {
		t.Fatal("pickFolder took an unknown download")
	}
	// Only while it waits for a folder: changing one is a move (ticket 07).
	if _, err := e.Call(nil, "pickFolder", []byte(fmt.Sprintf(`{"id":%q}`, id))); err == nil {
		t.Fatal("pickFolder took a download that has its folder")
	}
}

// handleWindow is a Downloads window that is open, with this handle.
type handleWindow uintptr

func (handleWindow) Open(string) error { return nil }
func (w handleWindow) Handle() uintptr { return uintptr(w) }
