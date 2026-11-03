package engine

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/atomicfile"
	"downloadmanagersquared/engine/internal/download"
)

// balloons records the balloons shown while the test runs.
func balloons(t *testing.T) *[]string {
	t.Helper()
	var shown []string
	old := notify
	notify = func(title, msg string, _ func()) { shown = append(shown, title+": "+msg) }
	t.Cleanup(func() { notify = old })
	return &shown
}

func TestUnreadableSettingsAreSetAsideAndDefaultsUsed(t *testing.T) {
	for name, content := range map[string]string{
		"empty":     "",
		"truncated": `{"interfaces": {}, "maxParallel": 3, "save`,
		"invalid":   `{"maxParallel": "three"}`,
	} {
		t.Run(name, func(t *testing.T) {
			shown := balloons(t)
			dir := t.TempDir()
			path := filepath.Join(dir, "settings.json")
			os.WriteFile(path, []byte(content), 0o644)
			e, err := open(dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Shutdown()
			if e.settings.MaxParallel != 1 || e.settings.ThresholdBytes != defaultSettings().ThresholdBytes {
				t.Fatalf("settings %+v, want the defaults", e.settings)
			}
			if b, _ := os.ReadFile(path + ".broken"); string(b) != content {
				t.Fatalf("settings.json.broken holds %q, want the old file", b)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("no fresh settings.json: %v", err)
			}
			if len(*shown) != 1 || !strings.Contains((*shown)[0], "settings.json.broken") {
				t.Fatalf("balloons %q, want one about settings.json.broken", *shown)
			}
		})
	}
}

func TestMissingSettingsAreNoNews(t *testing.T) {
	shown := balloons(t)
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	if len(*shown) != 0 {
		t.Fatalf("balloons %q on a first start", *shown)
	}
}

func TestBrokenStateFilesAreSetAsideWhileTheOthersLoad(t *testing.T) {
	shown := balloons(t)
	dir := t.TempDir()
	good := download.State{ID: "good", URL: "https://example.com/a.iso", Status: download.StatusPaused, Size: -1}
	if err := atomicfile.WriteJSON(filepath.Join(dir, "downloads", "good.json"), good); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "downloads", "bad1.json"), nil, 0o644)
	os.MkdirAll(filepath.Join(dir, "torrents"), 0o755)
	os.WriteFile(filepath.Join(dir, "torrents", "bad2.json"), []byte(`{"id": "bad2", "magnet`), 0o644)
	os.WriteFile(filepath.Join(dir, "downloads", "x.json.tmp"), []byte("{"), 0o644)

	e, err := open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	if _, err := e.queue.Get("good"); err != nil {
		t.Fatalf("the readable download wasn't restored: %v", err)
	}
	for _, f := range []string{"downloads/bad1.json.broken", "torrents/bad2.json.broken"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "downloads", "x.json.tmp")); !os.IsNotExist(err) {
		t.Error("a stale temp file is still there")
	}
	if len(*shown) != 1 || !strings.Contains((*shown)[0], "2 downloads couldn't be restored") {
		t.Fatalf("balloons %q, want one about 2 downloads", *shown)
	}
}

func TestUnreadablePIALoginAndTunnelAreSetAside(t *testing.T) {
	shown := balloons(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pia.dat"), []byte("not sealed"), 0o600)
	e, err := open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	conf := e.tunnelFile("wifi")
	e.Shutdown()
	if len(*shown) != 1 || !strings.Contains((*shown)[0], "sign in again") {
		t.Fatalf("balloons %q, want one about the PIA login", *shown)
	}
	os.MkdirAll(filepath.Dir(conf), 0o755)
	os.WriteFile(conf, []byte("[Interface]\nPrivateKey = nope\n"), 0o600)

	*shown = nil
	e, err = open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	if e.pia != nil || e.tunnels["wifi"] != nil {
		t.Fatal("loaded an unreadable login or tunnel")
	}
	for _, f := range []string{filepath.Join(dir, "pia.dat.broken"), conf + ".broken"} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if len(*shown) != 1 || !strings.Contains((*shown)[0], "VPN config for wifi") {
		t.Fatalf("balloons on the second start %q, want one about the tunnel", *shown)
	}
}

// closeWatcher is a listener that notes whether a download's state was saved
// by the time the pipe closed.
type closeWatcher struct {
	net.Listener
	state string
	saved bool
}

func (c *closeWatcher) Close() error {
	_, err := os.Stat(c.state)
	c.saved = err == nil
	return nil
}

func TestQuitSavesStateBeforeThePipeCloses(t *testing.T) {
	dir := t.TempDir()
	ln := &closeWatcher{}
	e, err := open(dir, ln)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := e.Add(AddRequest{URL: "https://example.com/a.iso"})
	ln.state = filepath.Join(dir, "downloads", id+".json")
	res, err := e.Call(nil, "quit", nil)
	if err != nil {
		t.Fatal(err)
	}
	if pid := res.(map[string]int)["pid"]; pid != os.Getpid() {
		t.Fatalf("quit answered pid %d, want %d", pid, os.Getpid())
	}
	select {
	case <-e.QuitRequested():
	case <-time.After(time.Second):
		t.Fatal("quit didn't ask the tray to quit")
	}
	e.Shutdown() // what the tray does on quit
	if !ln.saved {
		t.Fatal("the pipe closed before the download's state was saved")
	}
}
