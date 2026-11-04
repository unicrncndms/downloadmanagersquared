package engine

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeWindow is the Downloads window: it records the views asked for, and
// fails every Open with err (WebView2 missing) when err is set.
type fakeWindow struct {
	mu    sync.Mutex
	views []string
	err   error
}

func (w *fakeWindow) Open(view string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.views = append(w.views, view)
	return w.err
}

func (w *fakeWindow) opened() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.views...)
}

// browsersStarted replaces startBrowser for the test, recording each exe.
func browsersStarted(t *testing.T) chan string {
	t.Helper()
	started := make(chan string, 8)
	old := startBrowser
	startBrowser = func(exe, url string) error { started <- exe; return nil }
	t.Cleanup(func() { startBrowser = old })
	return started
}

func openWithWindow(t *testing.T, w Window) *Engine {
	t.Helper()
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Shutdown)
	e.SetWindow(w)
	return e
}

func TestOpenManagerOpensTheWindowWithTheViewAskedFor(t *testing.T) {
	w := &fakeWindow{}
	e := openWithWindow(t, w)
	e.OpenManager("")
	e.OpenManager("#settings")
	e.OpenManager("#pia")
	got, want := w.opened(), []string{"", "#settings", "#pia"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("the window was opened with %q, want %q", got, want)
	}
}

func TestASecondLaunchOpensTheDownloadsInTheWindow(t *testing.T) {
	w := &fakeWindow{}
	e := openWithWindow(t, w)
	x := connectExtension(t, e, fakeBrowserExe(t, "firefox.exe"), "")
	// What a second dms-engine.exe sends: ipc.Call("openManager"), no params.
	if _, err := e.Call(x.conn, "openManager", nil); err != nil {
		t.Fatal(err)
	}
	if got := w.opened(); len(got) != 1 || got[0] != "" {
		t.Fatalf("a second launch opened the window with %q, want the downloads (\"\")", got)
	}
}

func TestWithTheWindowWorkingNoBrowserIsAskedToShowAnything(t *testing.T) {
	w := &fakeWindow{}
	e := openWithWindow(t, w)
	started := browsersStarted(t)
	x := connectExtension(t, e, fakeBrowserExe(t, "firefox.exe"), "")

	e.OpenManager("")
	e.OpenManager("#settings")
	if x.next("openManager", 300*time.Millisecond) {
		t.Fatal("the extension was asked to show the downloads, though the window opened")
	}
	if len(started) != 0 {
		t.Fatalf("a browser was started (%s), though the window opened", <-started)
	}
}

func TestWithoutWebView2TheBrowserTheExtensionLastRanInIsStarted(t *testing.T) {
	w := &fakeWindow{err: errors.New("WebView2 could not start")}
	e := openWithWindow(t, w)
	started := browsersStarted(t)
	firefox := fakeBrowserExe(t, "firefox.exe")
	x := connectExtension(t, e, firefox, "")
	e.Disconnected(x.conn) // Firefox closed since

	e.OpenManager("")
	select {
	case exe := <-started:
		if exe != firefox {
			t.Fatalf("started %q, want the browser the extension last ran in", exe)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("with the window failing, no browser was started")
	}
	if got := w.opened(); len(got) != 1 {
		t.Fatalf("the window was tried %d times, want once", len(got))
	}
}

func TestWithoutWebView2AConnectedExtensionShowsTheView(t *testing.T) {
	w := &fakeWindow{err: errors.New("WebView2 could not start")}
	e := openWithWindow(t, w)
	started := browsersStarted(t)
	x := connectExtension(t, e, fakeBrowserExe(t, "firefox.exe"), "")

	raw, _ := json.Marshal(map[string]string{"hash": "#settings"})
	if _, err := e.Call(x.conn, "openManager", raw); err != nil {
		t.Fatal(err)
	}
	if !x.next("openManager", 2*time.Second) {
		t.Fatal("with the window failing, the connected extension wasn't asked to show the view")
	}
	if len(started) != 0 {
		t.Fatal("a browser was started though its extension is connected")
	}
}

func TestWithoutAWindowTheBrowserFallbackRuns(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	x := connectExtension(t, e, fakeBrowserExe(t, "firefox.exe"), "")
	e.OpenManager("")
	if !x.next("openManager", 2*time.Second) {
		t.Fatal("an engine given no window didn't fall back to the extension")
	}
}
