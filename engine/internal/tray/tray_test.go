package tray

import (
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

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

	m := menuFor(running, engine.SpeedBytes, used, now)
	if want := []string{"Ethernet: 7.1 MB/s", "VPN via Ethernet: 2.0 MB/s"}; !slices.Equal(m.Rows, want) {
		t.Fatalf("rows %q, want %q", m.Rows, want)
	}
	if !m.Pause || m.Resume || m.Status != "9.1 MB/s  ·  1m 30s left" {
		t.Fatalf("got %+v, want the speed and time left, and only Pause all", m)
	}

	// A second without connections keeps the line, so it doesn't blink.
	running.Downloads[0].Links["tun:eth"] = download.LinkSummary{}
	if m := menuFor(running, engine.SpeedBytes, used, now.Add(time.Second)); len(m.Rows) != 2 {
		t.Fatalf("rows %q a second after the tunnel went quiet, want it still listed", m.Rows)
	}
	if m := menuFor(running, engine.SpeedBytes, used, now.Add(linger+2*time.Second)); len(m.Rows) != 1 {
		t.Fatalf("rows %q long after the tunnel went quiet, want it gone", m.Rows)
	}

	paused := engine.StateEvent{Interfaces: ifaces, Downloads: []download.Summary{{Status: download.StatusPaused}}}
	if m := menuFor(paused, engine.SpeedBytes, used, now); len(m.Rows) != 0 || m.Status != "Paused" || m.Pause || !m.Resume {
		t.Fatalf("paused: got %+v, want status Paused, no rows, only Resume all", m)
	}
	if m := menuFor(engine.StateEvent{Interfaces: ifaces}, engine.SpeedBytes, used, now); len(m.Rows) != 0 || m.Status != "Idle" || m.Pause || m.Resume {
		t.Fatalf("idle: got %+v, want status Idle and nothing else", m)
	}
}

func TestSpeedInBytesOrBits(t *testing.T) {
	for _, c := range []struct {
		bps  float64
		unit string
		want string
	}{
		{0, engine.SpeedBytes, "0 B/s"},
		{1.5e6, engine.SpeedBytes, "1.5 MB/s"},
		{0, engine.SpeedBits, "0 bit/s"},
		{1.5e6, engine.SpeedBits, "12.0 Mbit/s"},
		{125e6, engine.SpeedBits, "1.0 Gbit/s"},
		{150e6, engine.SpeedBytes, "150 MB/s"}, // no decimal from 100, like fmtSpeed
		{12, engine.SpeedBytes, "12 B/s"},
		{1e6, "", "1.0 MB/s"}, // an unknown unit is bytes
	} {
		if got := Speed(c.bps, c.unit); got != c.want {
			t.Errorf("Speed(%v, %q) = %q, want %q", c.bps, c.unit, got, c.want)
		}
	}
}

func TestTheIconSaysDownloadingOrNeedsYou(t *testing.T) {
	run := download.Summary{Status: download.StatusRunning}
	for _, c := range []struct {
		name string
		s    engine.StateEvent
		want iconState
	}{
		{"idle", engine.StateEvent{}, iconIdle},
		{"paused", engine.StateEvent{Downloads: []download.Summary{{Status: download.StatusPaused}}}, iconIdle},
		{"downloading", engine.StateEvent{Active: 1, Downloads: []download.Summary{run}}, iconBusy},
		{"failed", engine.StateEvent{Downloads: []download.Summary{{Status: download.StatusFailed}}}, iconNeedsYou},
		{"waiting on a choice while another runs", engine.StateEvent{Active: 1, Downloads: []download.Summary{run, {Status: download.StatusChoosing}}}, iconNeedsYou},
	} {
		if got := iconFor(c.s); got != c.want {
			t.Errorf("%s: iconFor = %v, want %v", c.name, got, c.want)
		}
	}
	if string(icons[iconIdle]()) == string(icons[iconBusy]()) || string(icons[iconBusy]()) == string(icons[iconNeedsYou]()) {
		t.Error("two states share one icon")
	}
}

func TestTooltipKeepsWholeRowsWithinWindowsLimit(t *testing.T) {
	head := "DMS²  Downloading"
	long := strings.Repeat("é", 60)
	got := tooltip(head, []string{"Wi-Fi · 5 MB/s", long, "last"})
	if got != head+"\nWi-Fi · 5 MB/s" {
		t.Errorf("tooltip = %q, want the head and the first row only", got)
	}
	if n := len(utf16.Encode([]rune(tooltip(strings.Repeat("😀", 100), nil)))); n > 127 {
		t.Errorf("a long head is %d units", n)
	}
}
