package engine

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/resolver"
)

type fakeOwn struct {
	mu   sync.Mutex
	reqs []resolver.Request
}

func (f *fakeOwn) Request(r resolver.Request) { f.mu.Lock(); f.reqs = append(f.reqs, r); f.mu.Unlock() }
func (f *fakeOwn) Close()                     {}
func (f *fakeOwn) got() []resolver.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]resolver.Request{}, f.reqs...)
}

// With only Chrome connected a resolve runs in the engine's browser, with the
// download's cookies as the page's login; a Firefox download with Firefox
// connected still goes to Firefox, and Firefox's hello doesn't take the
// engine's resolves over.
func TestChromiumDownloadsResolveInTheEnginesBrowser(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	own := &fakeOwn{}
	e.own, e.ownHosts = own, map[string]bool{}

	no := false
	chromeExe := fakeBrowserExe(t, "chrome.exe")
	chrome := connectExtensionWith(t, e, helloParams{Browser: chromeExe, Resolves: &no})
	raw, _ := json.Marshal(AddRequest{URL: "https://dl.site.test/a.iso", PageURL: "https://site.test/a", Cookie: "sid=1; theme=dark"})
	res, err := e.Call(chrome.conn, "add", raw)
	if err != nil {
		t.Fatal(err)
	}
	id := res.(map[string]string)["id"]

	e.RequestResolve(id, "eth", "https://site.test/a", chromeExe)
	reqs := own.got()
	if len(reqs) != 1 || reqs[0].ID != id || reqs[0].PageURL != "https://site.test/a" {
		t.Fatalf("the engine's resolver got %+v", reqs)
	}
	if c := reqs[0].Cookies; len(c) != 2 || c[0].Name != "sid" || c[0].Value != "1" || c[0].Domain != "site.test" {
		t.Fatalf("cookies %+v, want the download's for site.test", c)
	}
	if chrome.next("resolve", 300*time.Millisecond) {
		t.Fatal("Chrome's extension got a resolve")
	}

	firefox := fakeBrowserExe(t, "firefox.exe")
	ff := connectExtension(t, e, firefox, "")
	if ff.next("resolve", 300*time.Millisecond) {
		t.Fatal("Firefox's hello took over the engine's resolve")
	}
	e.RequestResolve("d2", "eth", "https://example.com/b", firefox)
	if !ff.next("resolve", 2*time.Second) {
		t.Fatal("Firefox's extension didn't get its download's resolve")
	}
	if n := len(own.got()); n != 1 {
		t.Fatalf("the engine's resolver got %d resolves, want only Chrome's", n)
	}

	// The engine answers through the hooks.
	e.ownHooks().Failed(id, "eth", "no link")
	e.mu.Lock()
	_, pending := e.resolves[id+"|eth"]
	e.mu.Unlock()
	if pending {
		t.Fatal("a failed resolve stays pending")
	}
}

func TestPageCookiesOnlyForTheDownloadsSite(t *testing.T) {
	if c := pageCookies("a=1", "https://cdn.other.test/f", "https://site.test/p"); c != nil {
		t.Fatalf("another site's cookies copied: %+v", c)
	}
	c := pageCookies("a=1; b=x=y", "http://dl.site.test/f", "http://www.site.test/p")
	if len(c) != 2 || c[1].Value != "x=y" || c[1].Domain != "www.site.test" || c[0].Secure {
		t.Fatalf("got %+v", c)
	}
}
