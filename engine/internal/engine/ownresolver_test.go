package engine

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/resolver"
)

type fakeOwn struct {
	mu        sync.Mutex
	reqs      []resolver.Request
	cancelled []string
}

func (f *fakeOwn) Cancel(id string) {
	f.mu.Lock()
	f.cancelled = append(f.cancelled, id)
	f.mu.Unlock()
}

func (f *fakeOwn) Request(r resolver.Request) { f.mu.Lock(); f.reqs = append(f.reqs, r); f.mu.Unlock() }
func (f *fakeOwn) Close()                     {}
func (f *fakeOwn) got() []resolver.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]resolver.Request{}, f.reqs...)
}

// await is the resolver's requests once there are n, within 3 s.
func (f *fakeOwn) await(t *testing.T, n int) []resolver.Request {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if r := f.got(); len(r) >= n {
			return r
		}
	}
	t.Fatalf("the engine's resolver got %d requests, want %d", len(f.got()), n)
	return nil
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
	e.own = own

	no := false
	chromeExe := fakeBrowserExe(t, "chrome.exe")
	chrome := connectExtensionWith(t, e, helloParams{Browser: chromeExe, Resolves: &no})
	raw, _ := json.Marshal(AddRequest{URL: "https://dl.site.test/a.iso", PageURL: "https://site.test/a", Cookie: "sid=1; theme=dark"})
	res, err := e.Call(chrome.conn, "add", raw)
	if err != nil {
		t.Fatal(err)
	}
	id := res.(map[string]string)["id"]

	// Chrome is asked for the page's login, way back and accent, and its answer goes to the resolver.
	e.RequestResolve(id, "eth", "https://site.test/a", chromeExe)
	if !chrome.next("pageInfo", 2*time.Second) {
		t.Fatal("Chrome's extension wasn't asked for the page's cookies")
	}
	info, _ := json.Marshal(pageInfo{ID: id, Cookies: []resolver.Cookie{{Name: "login", Value: "x", URL: "https://site.test/"}},
		Pages: []string{"https://site.test/", "https://site.test/a"}, Tokens: json.RawMessage(`{"accent":"#123456"}`)})
	if _, err := e.Call(chrome.conn, "pageInfo", info); err != nil {
		t.Fatal(err)
	}
	reqs := own.await(t, 1)
	if reqs[0].ID != id || reqs[0].PageURL != "https://site.test/a" {
		t.Fatalf("the engine's resolver got %+v", reqs)
	}
	if c := reqs[0].Cookies; len(c) != 1 || c[0].Name != "login" || len(reqs[0].Pages) != 2 || string(reqs[0].Tokens.(json.RawMessage)) != `{"accent":"#123456"}` {
		t.Fatalf("got cookies %+v, pages %v, tokens %v: want Chrome's", c, reqs[0].Pages, reqs[0].Tokens)
	}
	if chrome.next("resolve", 300*time.Millisecond) {
		t.Fatal("Chrome's extension got a resolve")
	}

	// Without an answer in time, the download's own cookies are the login.
	old := pageInfoWait
	pageInfoWait = 100 * time.Millisecond
	t.Cleanup(func() { pageInfoWait = old })
	e.RequestResolve(id, "wifi", "https://site.test/a", chromeExe)
	reqs = own.await(t, 2)
	if c := reqs[1].Cookies; len(c) != 2 || c[0].Name != "sid" || c[0].Value != "1" || c[0].Domain != "site.test" {
		t.Fatalf("cookies %+v, want the download's for site.test", c)
	}

	// A recorded first click on an earlier page goes back to Chrome's capture.
	e.ownHooks().LearnStepBack(id, "https://site.test/")
	if !chrome.next("stepBack", 2*time.Second) {
		t.Fatal("Chrome wasn't told where the first click was")
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
	if n := len(own.got()); n != 2 {
		t.Fatalf("the engine's resolver got %d resolves, want only Chrome's", n)
	}

	// Removing the download stops its resolve in the engine's browser.
	if err := e.Remove(id, false); err != nil {
		t.Fatal(err)
	}
	own.mu.Lock()
	cancelled := own.cancelled
	own.mu.Unlock()
	if len(cancelled) != 1 || cancelled[0] != id {
		t.Fatalf("cancelled %v, want the removed download", cancelled)
	}

	// The engine answers through the hooks, and one for a removed download is dropped.
	e.ownHooks().Failed(id, "eth", "no link")
	e.mu.Lock()
	_, pending := e.resolves[id+"|eth"]
	e.mu.Unlock()
	if pending {
		t.Fatal("a removed download's resolve stays pending")
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
