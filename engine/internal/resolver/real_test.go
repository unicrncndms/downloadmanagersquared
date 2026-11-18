package resolver

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestARealBrowser drives the browser named by DMS_CDP_EXE (skipped without
// it): a context through a SOCKS server, a minimized window, the page script
// both ways and hidden from the page, the catch, a popup and the window moves.
func TestARealBrowser(t *testing.T) {
	exe := os.Getenv("DMS_CDP_EXE")
	if exe == "" {
		t.Skip("set DMS_CDP_EXE to a chrome.exe or msedge.exe")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dl":
			fmt.Fprint(w, `<html><body><a id=f href="/file">file</a> <a id=t target=_blank href="/dl2">tab</a></body></html>`)
		case "/dl2":
			fmt.Fprint(w, `<html><body>popup</body></html>`)
		case "/file":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="big.bin"`)
			w.Header().Set("Content-Length", fmt.Sprint(50<<20))
			buf := make([]byte, 64<<10)
			for i := 0; i < 800; i++ {
				if _, err := w.Write(buf); err != nil {
					return
				}
			}
		}
	}))
	defer srv.Close()
	// The real page script, from the folder DMS_PAGE_SCRIPT_DIR (extension/'s layout): a go test exe carries none.
	if dir := os.Getenv("DMS_PAGE_SCRIPT_DIR"); dir != "" {
		was := pageScript
		t.Cleanup(func() { pageScript = was })
		pageScript = func() (string, error) {
			ready, err := os.ReadFile(filepath.Join(dir, "lib", "ready.js"))
			if err != nil {
				return "", err
			}
			content, err := os.ReadFile(filepath.Join(dir, "resolver-content.js"))
			return prelude + string(ready) + "\n" + string(content), err
		}
	} else {
		t.Fatal("set DMS_PAGE_SCRIPT_DIR to a folder with lib/ready.js and resolver-content.js")
	}
	port, hosts := testSocks(t, srv.Listener.Addr().String())
	base := "http://dms.test:" + strings.Split(srv.Listener.Addr().String(), ":")[1]

	opened := make(chan [2]string, 4)
	caught := make(chan Catch, 1)
	msgs := make(chan string, 4)
	d, b, err := Start(exe, t.TempDir(), Events{
		Opened:  func(target, opener, url string) { opened <- [2]string{target, opener} },
		Caught:  func(_ string, c Catch) { caught <- c },
		Message: func(_, p string) { msgs <- p },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	ctx, err := d.Context("eth", port)
	if err != nil {
		t.Fatal(err)
	}
	page, err := d.Open(ctx, base+"/dl", false)
	if err != nil {
		t.Fatal(err)
	}
	if st := windowState(t, d, page); st != "minimized" {
		t.Errorf("opened %s, want minimized", st)
	}
	select {
	case o := <-opened:
		if o[0] != page || o[1] != "" {
			t.Errorf("opened %v", o)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no Opened")
	}
	waitFor(t, "page script in", func() bool {
		return d.Drive(page) == nil && mainEval(t, d, page, "document.readyState") == `"complete"`
	})
	viaProxy := false
	for len(hosts) > 0 && !viaProxy {
		viaProxy = <-hosts == "dms.test"
	}
	if !viaProxy {
		t.Error("the SOCKS server never got dms.test: the page did not go through it by name")
	}

	// Hidden from the page.
	if got := mainEval(t, d, page, "typeof browser + typeof __dmsDeliver + typeof __dmsSend"); got != `"undefinedundefinedundefined"` {
		t.Errorf("the page sees %s", got)
	}
	// Page script to engine, and back.
	d.mu.Lock()
	world := d.worlds[page]
	d.mu.Unlock()
	if r, err := d.evaluate(d.session(page), world, "window.__dmsResolver === true"); err != nil || !strings.Contains(string(r), "true") {
		t.Fatalf("resolver-content.js did not run: %s %v", r, err)
	}
	if _, err := d.evaluate(d.session(page), world, `browser.runtime.onMessage.addListener((m) => browser.runtime.sendMessage({ echo: m.n }))`); err != nil {
		t.Fatal(err)
	}
	if err := d.Send(page, map[string]int{"n": 42}); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-msgs:
		if m != `{"echo":42}` {
			t.Errorf("message %s", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no message from the page script")
	}

	// The catch, from a click in the page script's world (as a replay's).
	d.evaluate(d.session(page), world, `document.getElementById("f").click()`)
	select {
	case c := <-caught:
		if c.URL != base+"/file" || c.Referer != base+"/dl" {
			t.Errorf("catch %+v", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no catch")
	}

	// A replayed click that opens a tab: not popup-blocked, same context.
	d.evaluate(d.session(page), world, `document.getElementById("t").click()`)
	select {
	case o := <-opened:
		if o[1] != page {
			t.Errorf("popup's opener %q, want %q", o[1], page)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the replayed click's tab was blocked")
	}

	if err := d.Show(page, Bounds{100, 100, 900, 700}, true); err != nil {
		t.Fatal(err)
	}
	if st := windowState(t, d, page); st != "normal" {
		t.Errorf("shown %s", st)
	}
	if err := d.Minimize(page); err != nil {
		t.Fatal(err)
	}
	if st := windowState(t, d, page); st != "minimized" {
		t.Errorf("minimized %s", st)
	}
	d.DropContext("eth")
}

func windowState(t *testing.T, d *Driver, target string) string {
	t.Helper()
	w, err := d.window(target)
	if err != nil {
		t.Fatal(err)
	}
	r, err := d.call("", "Browser.getWindowBounds", map[string]any{"windowId": w})
	if err != nil {
		t.Fatal(err)
	}
	var b struct {
		Bounds struct{ WindowState string }
	}
	json.Unmarshal(r, &b)
	return b.Bounds.WindowState
}

// mainEval runs js in the page's own world.
func mainEval(t *testing.T, d *Driver, target, js string) string {
	t.Helper()
	r, err := d.call(d.session(target), "Runtime.evaluate", map[string]any{"expression": js, "returnByValue": true})
	if err != nil {
		return ""
	}
	var v struct {
		Result struct{ Value json.RawMessage }
	}
	json.Unmarshal(r, &v)
	return string(v.Result.Value)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out: " + what)
}

// testSocks is a SOCKS5 server that sends dms.test to target and refuses the
// rest; hosts gets each name asked for.
func testSocks(t *testing.T, target string) (int, chan string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	hosts := make(chan string, 64)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				b := make([]byte, 262)
				if _, err := io.ReadFull(c, b[:2]); err != nil {
					return
				}
				io.ReadFull(c, b[:b[1]])
				c.Write([]byte{5, 0})
				io.ReadFull(c, b[:4])
				host := ""
				switch b[3] {
				case 1:
					io.ReadFull(c, b[:4])
					host = net.IP(b[:4]).String()
				case 3:
					io.ReadFull(c, b[:1])
					n := b[0]
					io.ReadFull(c, b[:n])
					host = string(b[:n])
				case 4:
					io.ReadFull(c, b[:16])
					host = net.IP(b[:16]).String()
				}
				io.ReadFull(c, b[:2])
				_ = binary.BigEndian.Uint16(b[:2])
				select {
				case hosts <- host:
				default:
				}
				if host != "dms.test" {
					c.Write([]byte{5, 2, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				u, err := net.Dial("tcp", target)
				if err != nil {
					c.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				defer u.Close()
				c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				go io.Copy(u, c)
				io.Copy(c, u)
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, hosts
}

// TestARealReplay runs a whole resolve in the browser named by DMS_CDP_EXE: a
// stored recipe, the real page script clicking it, the catch and the hook.
func TestARealReplay(t *testing.T) {
	exe, dir := os.Getenv("DMS_CDP_EXE"), os.Getenv("DMS_PAGE_SCRIPT_DIR")
	if exe == "" || dir == "" {
		t.Skip("set DMS_CDP_EXE and DMS_PAGE_SCRIPT_DIR")
	}
	was := pageScript
	t.Cleanup(func() { pageScript = was })
	pageScript = func() (string, error) {
		ready, err := os.ReadFile(filepath.Join(dir, "lib", "ready.js"))
		if err != nil {
			return "", err
		}
		content, err := os.ReadFile(filepath.Join(dir, "resolver-content.js"))
		return prelude + string(ready) + "\n" + string(content), err
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/f":
			fmt.Fprint(w, `<html><body><p>Your file</p><a id=go href="/wait">Free download</a></body></html>`)
		case "/wait":
			fmt.Fprint(w, `<html><body><a id=get href="/file">Get it now</a></body></html>`)
		case "/file":
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("Content-Disposition", `attachment; filename="f.zip"`)
			w.Header().Set("Content-Length", fmt.Sprint(20<<20))
			w.Write(make([]byte, 1<<20))
		}
	}))
	defer srv.Close()
	port, _ := testSocks(t, srv.Listener.Addr().String())
	base := "http://dms.test:" + strings.Split(srv.Listener.Addr().String(), ":")[1]

	recipes, _ := OpenRecipes(filepath.Join(t.TempDir(), "recipes.json"))
	recipes.Set("dms.test", &Recipe{Steps: []Step{
		{Tag: "a", ID: "go", Text: "Free download", Selector: "#go", Wait: 500},
		{Tag: "a", ID: "get", Text: "Get it now", Selector: "#get", Wait: 500},
	}, DownloadWait: 500})
	done := make(chan string, 2)
	profile := t.TempDir()
	r := &Resolver{
		Recipes: recipes, Idle: time.Second,
		Start: func(ev Events) (*Driver, Browser, error) { return Start(exe, profile, traced(t, ev)) },
		Hooks: Hooks{
			Resolved: func(id, iface, url, cookie, referer string) { done <- "resolved " + url + " referer=" + referer },
			Failed:   func(id, iface, why string) { done <- "failed " + why },
		},
	}
	defer r.Close()
	start := time.Now()
	r.Request(Request{ID: "d1", Iface: "eth", IfaceName: "Ethernet", PageURL: base + "/f", SocksPort: port})
	select {
	case s := <-done:
		if s != "resolved "+base+"/file referer="+base+"/wait" {
			t.Fatal(s)
		}
		t.Logf("resolved in %v", time.Since(start))
	case <-time.After(60 * time.Second):
		t.Fatal("no end in 60 s")
	}
	rec := recipes.Get("dms.test")
	t.Logf("measured waits: %d %d, download %d", rec.Steps[0].Wait, rec.Steps[1].Wait, rec.DownloadWait)
}

// traced logs each event before passing it on.
func traced(t *testing.T, ev Events) Events {
	return Events{
		Opened:  func(a, b, c string) { t.Logf("opened %s by %q %s", a, b, c); ev.Opened(a, b, c) },
		Loading: func(a, b string) { t.Logf("loading %s %s", a, b); ev.Loading(a, b) },
		Loaded:  func(a, b string) { t.Logf("loaded %s %s", a, b); ev.Loaded(a, b) },
		Closed:  func(a string) { t.Logf("closed %s", a); ev.Closed(a) },
		Caught:  func(a string, c Catch) { t.Logf("caught %s %s", a, c.URL); ev.Caught(a, c) },
		Message: func(a, b string) { t.Logf("message %s %s", a, b); ev.Message(a, b) },
	}
}
