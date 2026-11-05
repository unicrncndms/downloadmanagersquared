package engine

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
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

// A download saved before cookies were encrypted is rewritten encrypted on the
// first save after loading, not only once it changes or the engine exits.
func TestPlainCookiesFromAnOlderVersionAreEncryptedRightAway(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "downloads", "old.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	old := `{"id":"old","url":"https://example.com/a.iso","status":"paused","size":-1,` +
		`"cookie":"session=PLAINMAIN","links":{"eth":{"url":"https://example.com/a.iso","cookie":"session=PLAINLINK"}}}`
	os.WriteFile(path, []byte(old), 0o644)

	e, err := open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	e.queue.Save() // one tick of Run, without the download changing

	b, _ := os.ReadFile(path)
	if bytes.Contains(b, []byte("PLAIN")) {
		t.Fatalf("still readable on disk after a save: %s", b)
	}
	if bytes.Count(b, []byte(`"dpapi:`)) != 2 {
		t.Fatalf("no encrypted cookie on disk: %s", b)
	}
}

// A download file that can't be read is set aside without its cookies, and
// one set aside by an older version loses them at the next start.
func TestSetAsideDownloadFilesKeepNoCookies(t *testing.T) {
	dir := t.TempDir()
	dl := filepath.Join(dir, "downloads")
	os.MkdirAll(dl, 0o755)
	os.WriteFile(filepath.Join(dl, "bad.json"), []byte(
		`{"id":"bad","cookie":"session=SECRETMAIN","links":{"eth":{"cookie" : "dpapi:SECRETSEALED","url":"https://example.com/a`), 0o644)
	os.WriteFile(filepath.Join(dl, "cut.json"), []byte(`{"id":"cut","cookie":"session=SECRETCUT`), 0o644)
	os.WriteFile(filepath.Join(dl, "older.json.broken"), []byte(`{"id":"older","cookie":"a=SECRETOLD; b=\"q\"","url":"https://example.com/b"`), 0o644)

	e, err := open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	for _, f := range []string{"bad.json.broken", "cut.json.broken", "older.json.broken"} {
		b, err := os.ReadFile(filepath.Join(dl, f))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if bytes.Contains(b, []byte("SECRET")) {
			t.Errorf("%s still holds a cookie: %s", f, b)
		}
		if !bytes.Contains(b, []byte(`"id":"`)) {
			t.Errorf("%s lost the rest of the file: %s", f, b)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dl, "older.json.broken")); !bytes.Contains(b, []byte(`"url":"https://example.com/b"`)) {
		t.Errorf("the text after the cookie is gone: %s", b)
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
	conf := filepath.Join(dir, "tunnels", hex.EncodeToString([]byte("wifi"))+".conf")
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
	if _, ok := e.tunnelStatus("wifi"); ok || e.vpn.PIA().Configured {
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

func TestACompletedDownloadKeepsNoCookie(t *testing.T) {
	data := bytes.Repeat([]byte("dms"), 100000)
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = append(sent, r.Header.Get("Cookie"))
		http.ServeContent(w, r, "a.bin", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	e := loopbackEngine(t)
	id, err := e.Add(AddRequest{URL: srv.URL + "/a.bin", Cookie: "session=secret"})
	if err != nil {
		t.Fatal(err)
	}
	e.queue.Save()
	state := filepath.Join(e.dir, "downloads", id+".json")
	b, _ := os.ReadFile(state)
	if bytes.Contains(b, []byte("secret")) {
		t.Fatalf("a queued download's cookie is readable on disk:\n%s", b)
	}
	var saved download.State
	if json.Unmarshal(b, &saved) != nil || saved.Cookie != "session=secret" {
		t.Fatal("a queued download doesn't keep its cookie to resume with")
	}
	d, _ := e.get(id)
	waitFor(t, "the download", 30*time.Second, func() bool {
		e.schedule()
		return d.Status() == download.StatusCompleted
	})
	if len(sent) == 0 || sent[0] != "session=secret" {
		t.Fatalf("the server got cookies %q", sent)
	}
	e.queue.Save()
	if b, _ := os.ReadFile(state); bytes.Contains(b, []byte("secret")) {
		t.Fatalf("the completed download's state still holds the cookie:\n%s", b)
	}
}
