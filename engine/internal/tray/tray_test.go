package tray

import (
	"testing"

	"downloadmanagersquared/engine/internal/engine"
	"downloadmanagersquared/engine/internal/window"
)

type fakeWindow struct{ views []string }

func (w *fakeWindow) Open(view string) error { w.views = append(w.views, view); return nil }

func TestTheMenuOpensTheWindowAtTheRightView(t *testing.T) {
	e, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	w := &fakeWindow{}
	e.SetWindow(w)

	openDownloads(e)
	openSettings(e)
	if len(w.views) != 2 || w.views[0] != "" || w.views[1] != window.Settings {
		t.Fatalf("Open downloads, then Connections & settings, opened the window with %q, want [\"\" %q]", w.views, window.Settings)
	}
}
