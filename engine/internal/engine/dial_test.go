package engine

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/winutil"
)

// windowClient is what the Downloads window does with Dial: it sends request
// lines and reads every line back, and never says hello.
type windowClient struct {
	c     net.Conn
	lines chan map[string]json.RawMessage
	seen  map[string]bool // events skipped while waiting for something else
}

func dialWindow(t *testing.T, e *Engine) *windowClient {
	t.Helper()
	w := &windowClient{c: e.Dial(), lines: make(chan map[string]json.RawMessage, 64), seen: map[string]bool{}}
	t.Cleanup(func() { w.c.Close() })
	go func() {
		r := bufio.NewReader(w.c)
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				return
			}
			var m map[string]json.RawMessage
			if json.Unmarshal(line, &m) == nil {
				w.lines <- m
			}
		}
	}()
	return w
}

func (w *windowClient) send(t *testing.T, id int, method string, params any) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if _, err := w.c.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

// next is the next line for which keep is true, skipping the others; false within wait.
func (w *windowClient) next(wait time.Duration, keep func(map[string]json.RawMessage) bool) (map[string]json.RawMessage, bool) {
	deadline := time.After(wait)
	for {
		select {
		case m := <-w.lines:
			if keep(m) {
				return m, true
			}
			var ev string
			if json.Unmarshal(m["event"], &ev) == nil {
				w.seen[ev] = true
			}
		case <-deadline:
			return nil, false
		}
	}
}

func (w *windowClient) answer(t *testing.T, id int) json.RawMessage {
	t.Helper()
	m, ok := w.next(2*time.Second, func(m map[string]json.RawMessage) bool { return string(m["id"]) == itoa(id) })
	if !ok {
		t.Fatalf("no answer to request %d", id)
	}
	if e := m["error"]; e != nil {
		t.Fatalf("request %d failed: %s", id, e)
	}
	return m["result"]
}

func (w *windowClient) event(name string, wait time.Duration) bool {
	if w.seen[name] {
		delete(w.seen, name)
		return true
	}
	_, ok := w.next(wait, func(m map[string]json.RawMessage) bool { return string(m["event"]) == `"`+name+`"` })
	return ok
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestTheWindowsClientGetsBroadcastsButNoResolves(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	old := startBrowser
	startBrowser = func(exe, url string) error { return nil }
	t.Cleanup(func() { startBrowser = old })
	w := dialWindow(t, e)

	w.send(t, 1, "getState", nil) // the pipe is served: requests are answered
	w.answer(t, 1)
	e.broadcast("state", e.Snapshot()) // as Run does each second the state changed
	if !w.event("state", 2*time.Second) {
		t.Fatal("the window didn't get the state broadcast")
	}
	w.send(t, 2, "setSettings", map[string]any{"maxParallel": 2})
	w.answer(t, 2)
	if !w.event("settings", 2*time.Second) {
		t.Fatal("the window didn't get the settings broadcast")
	}

	firefox := fakeBrowserExe(t, "firefox.exe")
	e.RequestResolve("d1", "eth", "https://example.com/a", firefox)
	e.RequestResolve("d2", "eth", "https://example.com/b", "")
	if w.event("resolve", 300*time.Millisecond) {
		t.Fatal("the window got a resolve, which is only for the extension")
	}
}

func TestTheWindowDoesNotReplaceTheBrowsersUserAgent(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	connectExtension(t, e, fakeBrowserExe(t, "firefox.exe"), "Firefox UA")
	w := dialWindow(t, e)
	w.send(t, 1, "getState", nil)
	w.answer(t, 1)
	e.mu.Lock()
	ua := e.userAgent
	e.mu.Unlock()
	if ua != "Firefox UA" {
		t.Fatalf("user agent is %q, want the browser's", ua)
	}
}

func TestGetAutostartAndVersionAnswer(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	w := dialWindow(t, e)

	w.send(t, 1, "version", nil)
	var v string
	if err := json.Unmarshal(w.answer(t, 1), &v); err != nil || v != Version {
		t.Fatalf("version is %q (%v), want %q", v, err, Version)
	}
	w.send(t, 2, "getAutostart", nil)
	var on bool
	raw := w.answer(t, 2)
	if err := json.Unmarshal(raw, &on); err != nil && raw != nil {
		t.Fatalf("getAutostart answered %s: %v", raw, err)
	}
	if on != winutil.Autostart() {
		t.Fatalf("getAutostart is %v, want %v", on, winutil.Autostart())
	}
}

// An idle engine sends its state once, not every second: nothing changed, so
// the pages have nothing to redraw.
func TestAnUnchangedStateIsNotSentAgain(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	w := dialWindow(t, e)
	w.send(t, 1, "getState", nil)
	w.answer(t, 1) // connected before the first tick
	go e.Run()
	n := 0
	for w.event("state", 3500*time.Millisecond) {
		n++
	}
	if n != 1 {
		t.Fatalf("got %d state events from an idle engine in three ticks, want the first only", n)
	}
}
