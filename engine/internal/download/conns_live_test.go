package download

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// setConns changes an interface's connection setting, as the settings page does.
func (e *testEnv) setConns(id string, n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.ifaces {
		if e.ifaces[i].ID == id {
			e.ifaces[i].Conns = n
		}
	}
}

func waitConns(t *testing.T, d *Download, id string, ok func(int) bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok(d.Summary().Links[id].Conns) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("connections on %s: %d, want %s", id, d.Summary().Links[id].Conns, what)
}

func TestConnectionSettingAppliesToARunningDownload(t *testing.T) {
	data := randomData(64 << 20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "big.bin", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	env := &testEnv{ifaces: []Iface{iface("eth", 4, 20*time.Millisecond)}, main: "eth", finished: make(chan struct{})}
	d := New(env, State{ID: "live", URL: srv.URL + "/big.bin", Dir: t.TempDir(), Size: -1})
	d.Start()
	defer d.Stop(StatusPaused)

	waitConns(t, d, "eth", func(n int) bool { return n == 4 }, "4 at start")
	env.setConns("eth", 1)
	waitConns(t, d, "eth", func(n int) bool { return n == 1 }, "1 after lowering the setting")
	env.setConns("eth", 6)
	waitConns(t, d, "eth", func(n int) bool { return n == 6 }, "6 after raising the setting")
}
