package tray

import (
	"slices"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/download"
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

func TestTheMenuShowsOnlyTheInterfacesCarryingDownloads(t *testing.T) {
	now := time.Now()
	used := map[string]time.Time{}
	ifaces := []engine.IfaceView{
		{ID: "eth", Name: "Ethernet", Present: true, Enabled: true, Speed: 7.1e6},
		{ID: "wifi", Name: "Wi-Fi", Present: true, Enabled: true},
		{ID: "tun:eth", Name: "VPN via Ethernet", Present: true, Enabled: true, Tunnel: true, Speed: 2e6},
	}
	running := engine.StateEvent{Active: 1, Speed: 9.1e6, ETA: 90, Interfaces: ifaces, Downloads: []download.Summary{{
		Status: download.StatusRunning,
		Links:  map[string]download.LinkSummary{"eth": {Conns: 4, Speed: 7.1e6}, "tun:eth": {Conns: 2, Speed: 2e6}, "wifi": {}},
	}}}

	m := menuFor(running, used, now)
	if want := []string{"Ethernet: 7.1 MB/s", "VPN via Ethernet: 2.0 MB/s"}; !slices.Equal(m.Rows, want) {
		t.Fatalf("rows %q, want %q", m.Rows, want)
	}
	if !m.Pause || m.Resume || m.Status != "9.1 MB/s  ·  1m 30s left" {
		t.Fatalf("got %+v, want the speed and time left, and only Pause all", m)
	}

	// A second without connections keeps the line, so it doesn't blink.
	running.Downloads[0].Links["tun:eth"] = download.LinkSummary{}
	if m := menuFor(running, used, now.Add(time.Second)); len(m.Rows) != 2 {
		t.Fatalf("rows %q a second after the tunnel went quiet, want it still listed", m.Rows)
	}
	if m := menuFor(running, used, now.Add(linger+2*time.Second)); len(m.Rows) != 1 {
		t.Fatalf("rows %q long after the tunnel went quiet, want it gone", m.Rows)
	}

	paused := engine.StateEvent{Interfaces: ifaces, Downloads: []download.Summary{{Status: download.StatusPaused}}}
	if m := menuFor(paused, used, now); len(m.Rows) != 0 || m.Status != "Paused" || m.Pause || !m.Resume {
		t.Fatalf("paused: got %+v, want status Paused, no rows, only Resume all", m)
	}
	if m := menuFor(engine.StateEvent{Interfaces: ifaces}, used, now); len(m.Rows) != 0 || m.Status != "Idle" || m.Pause || m.Resume {
		t.Fatalf("idle: got %+v, want status Idle and nothing else", m)
	}
}
