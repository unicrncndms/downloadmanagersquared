package engine

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/ipc"
)

// fakeExtension is an extension connected from the browser at exe: the engine
// writes to it through conn, the test reads what arrives from events.
type fakeExtension struct {
	conn     *ipc.Conn
	events   chan string          // names of events received
	resolves chan json.RawMessage // the data of each resolve event
}

func connectExtension(t *testing.T, e *Engine, exe, userAgent string) *fakeExtension {
	t.Helper()
	return connectExtensionWith(t, e, helloParams{UserAgent: userAgent, Browser: exe})
}

func connectExtensionWith(t *testing.T, e *Engine, hp helloParams) *fakeExtension {
	t.Helper()
	ours, theirs := net.Pipe()
	t.Cleanup(func() { ours.Close(); theirs.Close() })
	x := &fakeExtension{conn: ipc.NewConn(ours), events: make(chan string, 16), resolves: make(chan json.RawMessage, 16)}
	go func() {
		r := bufio.NewReader(theirs)
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				return
			}
			var ev struct {
				Event string          `json:"event"`
				Data  json.RawMessage `json:"data"`
			}
			if json.Unmarshal(line, &ev) == nil && ev.Event != "" {
				if ev.Event == "resolve" {
					select {
					case x.resolves <- ev.Data:
					default:
					}
				}
				x.events <- ev.Event
			}
		}
	}()
	e.Connected(x.conn)
	params, _ := json.Marshal(hp)
	if _, err := e.Call(x.conn, "hello", params); err != nil {
		t.Fatal(err)
	}
	return x
}

// next is the next event named name the extension gets, or "" within wait.
func (x *fakeExtension) next(name string, wait time.Duration) bool {
	deadline := time.After(wait)
	for {
		select {
		case ev := <-x.events:
			if ev == name {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func fakeBrowserExe(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolvingGoesToTheBrowserTheDownloadCameFrom(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	firefox, librewolf := fakeBrowserExe(t, "firefox.exe"), fakeBrowserExe(t, "librewolf.exe")
	ff := connectExtension(t, e, firefox, "Firefox UA")
	lw := connectExtension(t, e, librewolf, "LibreWolf UA") // connected last

	raw, _ := json.Marshal(AddRequest{URL: "https://example.com/a.iso", PageURL: "https://example.com/a"})
	res, err := e.Call(ff.conn, "add", raw)
	if err != nil {
		t.Fatal(err)
	}
	id := res.(map[string]string)["id"]
	d, err := e.get(id)
	if err != nil {
		t.Fatal(err)
	}
	if st := d.State(); st.Browser != firefox || st.UserAgent != "Firefox UA" {
		t.Fatalf("the download remembers browser %q with user agent %q, want Firefox's", st.Browser, st.UserAgent)
	}

	e.RequestResolve(id, "eth", "https://example.com/a", firefox)
	if !ff.next("resolve", 2*time.Second) {
		t.Fatal("Firefox's extension didn't get the resolve")
	}
	if lw.next("resolve", 300*time.Millisecond) {
		t.Fatal("LibreWolf's extension got a resolve for a download from Firefox")
	}
}

func TestAResolveForABrowserNotRunningStartsItAndWaitsForIt(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	started := make(chan string, 4)
	old := startBrowser
	startBrowser = func(exe, url string) error { started <- exe; return nil }
	t.Cleanup(func() { startBrowser = old })
	firefox, librewolf := fakeBrowserExe(t, "firefox.exe"), fakeBrowserExe(t, "librewolf.exe")
	lw := connectExtension(t, e, librewolf, "")

	e.RequestResolve("d1", "eth", "https://example.com/a", firefox)
	e.RequestResolve("d1", "wifi", "https://example.com/a", firefox) // a second link: start it once
	select {
	case exe := <-started:
		if exe != firefox {
			t.Fatalf("started %q, want Firefox", exe)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Firefox wasn't started")
	}
	if len(started) != 0 {
		t.Fatal("Firefox was started twice")
	}
	if lw.next("resolve", 300*time.Millisecond) {
		t.Fatal("the running LibreWolf got a resolve meant for Firefox")
	}
	ff := connectExtension(t, e, firefox, "")
	if !ff.next("resolve", 2*time.Second) {
		t.Fatal("Firefox didn't get the waiting resolve when its extension connected")
	}
}

func TestAResolveForAnUninstalledBrowserGoesToAnyBrowser(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	lw := connectExtension(t, e, fakeBrowserExe(t, "librewolf.exe"), "")
	e.RequestResolve("d1", "eth", "https://example.com/a", filepath.Join(t.TempDir(), "gone", "firefox.exe"))
	if !lw.next("resolve", 2*time.Second) {
		t.Fatal("with the download's browser gone, the running one didn't get the resolve")
	}
}

// A resolve carries the cookie store the download came from, so the
// extension can start the container with the user's login for the site.
func TestAResolveCarriesTheDownloadsCookieStore(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	firefox := fakeBrowserExe(t, "firefox.exe")
	ff := connectExtension(t, e, firefox, "Firefox UA")
	raw, _ := json.Marshal(AddRequest{URL: "https://example.com/a.iso", PageURL: "https://example.com/a", CookieStore: "firefox-container-3"})
	res, err := e.Call(ff.conn, "add", raw)
	if err != nil {
		t.Fatal(err)
	}
	id := res.(map[string]string)["id"]
	e.RequestResolve(id, "eth", "https://example.com/a", firefox)
	select {
	case data := <-ff.resolves:
		var req resolveReq
		if err := json.Unmarshal(data, &req); err != nil || req.CookieStore != "firefox-container-3" {
			t.Fatalf("resolve %s: cookie store %q, want firefox-container-3 (%v)", data, req.CookieStore, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no resolve")
	}
}

// Setup asks which browsers' extensions have said hello, to tick them.
func TestExtensionBrowsersListsTheOnesThatSaidHello(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	firefox := fakeBrowserExe(t, "firefox.exe")
	connectExtension(t, e, firefox, "")
	got, err := e.Call(nil, "extensionBrowsers", nil)
	if list, _ := got.([]string); err != nil || len(list) != 1 || list[0] != firefox {
		t.Fatalf("extensionBrowsers = %v, %v", got, err)
	}
}

// A browser that said resolves: false (Chromium) is never sent a resolve; a hello
// without the field counts as able, so older Firefox builds keep working.
func TestAResolveSkipsExtensionsThatCannotResolve(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	no := false
	chrome := connectExtensionWith(t, e, helloParams{Browser: fakeBrowserExe(t, "chrome.exe"), Resolves: &no})
	e.RequestResolve("d1", "eth", "https://example.com/a", "")
	if chrome.next("resolve", 300*time.Millisecond) {
		t.Fatal("a browser that can't resolve got a resolve")
	}
	e.mu.Lock()
	only := e.onlyNonResolvingLocked()
	e.mu.Unlock()
	if !only {
		t.Fatal("not reported as having no resolving extension")
	}
	ff := connectExtension(t, e, fakeBrowserExe(t, "firefox.exe"), "") // no resolves field
	e.RequestResolve("d2", "eth", "https://example.com/b", "")
	if !ff.next("resolve", 2*time.Second) {
		t.Fatal("the Firefox-like extension got no resolve")
	}
	if chrome.next("resolve", 300*time.Millisecond) {
		t.Fatal("a browser that can't resolve got a resolve")
	}
}
