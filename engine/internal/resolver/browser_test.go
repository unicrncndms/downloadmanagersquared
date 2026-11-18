package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"downloadmanagersquared/engine/internal/cdp"
)

type sent struct {
	session, method string
	params          map[string]any
}

// fakeBrowser answers each call with reply(method, params), "{}" by default.
type fakeBrowser struct {
	mu    sync.Mutex
	calls []sent
	reply func(method string, p map[string]any) (string, error)
}

func (f *fakeBrowser) Call(_ context.Context, session, method string, params any) (json.RawMessage, error) {
	b, _ := json.Marshal(params)
	var p map[string]any
	json.Unmarshal(b, &p)
	f.mu.Lock()
	f.calls = append(f.calls, sent{session, method, p})
	f.mu.Unlock()
	if f.reply != nil {
		r, err := f.reply(method, p)
		if r != "" || err != nil {
			return json.RawMessage(r), err
		}
	}
	return json.RawMessage(`{}`), nil
}

func (f *fakeBrowser) methods(session string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if c.session == session {
			out = append(out, c.method)
		}
	}
	return out
}

func (f *fakeBrowser) last(method string) *sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].method == method {
			return &f.calls[i]
		}
	}
	return nil
}

func ev(method, session string, params any) cdp.Event {
	b, _ := json.Marshal(params)
	return cdp.Event{Method: method, SessionID: session, Params: b}
}

func driver(t *testing.T, events Events) (*Driver, *fakeBrowser) {
	t.Helper()
	f := &fakeBrowser{}
	n := 0
	f.reply = func(m string, p map[string]any) (string, error) {
		if m == "Target.createBrowserContext" {
			n++
			return `{"browserContextId":"ctx` + string(rune('0'+n)) + `"}`, nil
		}
		return "", nil
	}
	d := newDriver(events)
	d.b = f
	return d, f
}

func TestAContextPerInterfaceThroughItsSocksPortAndANewOneWhenThePortChanges(t *testing.T) {
	d, f := driver(t, Events{})
	a, _ := d.Context("eth", 4001)
	if again, _ := d.Context("eth", 4001); again != a {
		t.Fatal("a second context for the same interface")
	}
	c := f.last("Target.createBrowserContext")
	if c.params["proxyServer"] != "socks5://127.0.0.1:4001" || c.params["proxyBypassList"] != "<-loopback>" {
		t.Fatalf("context params %v", c.params)
	}
	if dl := f.last("Browser.setDownloadBehavior"); dl.params["behavior"] != "deny" || dl.params["browserContextId"] != a {
		t.Fatalf("downloads not denied: %v", dl.params)
	}
	b, _ := d.Context("eth", 4002)
	if b == a {
		t.Fatal("a new SOCKS port kept the old context")
	}
	if disp := f.last("Target.disposeBrowserContext"); disp == nil || disp.params["browserContextId"] != a {
		t.Fatal("the old context was not disposed")
	}
}

func TestAContextWhoseDownloadsCannotBeDeniedIsNotUsed(t *testing.T) {
	d, f := driver(t, Events{})
	inner := f.reply
	f.reply = func(m string, p map[string]any) (string, error) {
		if m == "Browser.setDownloadBehavior" {
			return "", errors.New("nope")
		}
		return inner(m, p)
	}
	if _, err := d.Context("eth", 4001); err == nil {
		t.Fatal("no error")
	}
	if f.last("Target.disposeBrowserContext") == nil {
		t.Fatal("the half-made context was left")
	}
}

func TestOurPagesAreWatchedOthersRunUnwatched(t *testing.T) {
	d, f := driver(t, Events{})
	ctx, _ := d.Context("eth", 4001)
	d.event(ev("Target.attachedToTarget", "", map[string]any{"sessionId": "s1", "targetInfo": map[string]any{"targetId": "p1", "type": "page", "browserContextId": ctx}}))
	got := strings.Join(f.methods("s1"), ",")
	if got != "Fetch.enable,Target.setAutoAttach,Page.enable,Runtime.runIfWaitingForDebugger" {
		t.Fatalf("our page got %s", got)
	}
	d.event(ev("Target.attachedToTarget", "", map[string]any{"sessionId": "s2", "targetInfo": map[string]any{"targetId": "x", "type": "page", "browserContextId": "the-users"}}))
	if got := strings.Join(f.methods("s2"), ","); got != "Runtime.runIfWaitingForDebugger" {
		t.Fatalf("another page got %s", got)
	}
	if det := f.last("Target.detachFromTarget"); det == nil || det.params["sessionId"] != "s2" {
		t.Fatal("another page stayed attached")
	}
}

func TestTheCatchDropsTheDownloadAndNamesItsPageEvenFromAnIframe(t *testing.T) {
	var caught []string
	var c Catch
	d, f := driver(t, Events{Caught: func(target string, got Catch) { caught = append(caught, target); c = got }})
	ctx, _ := d.Context("eth", 4001)
	d.event(ev("Target.attachedToTarget", "", map[string]any{"sessionId": "s1", "targetInfo": map[string]any{"targetId": "p1", "type": "page", "browserContextId": ctx}}))
	d.event(ev("Target.attachedToTarget", "s1", map[string]any{"sessionId": "s1f", "targetInfo": map[string]any{"targetId": "f1", "type": "iframe", "browserContextId": ctx}}))

	page := map[string]any{"requestId": "r1", "request": map[string]any{"url": "https://h/page"}, "responseStatusCode": 200,
		"responseHeaders": []any{map[string]any{"name": "Content-Type", "value": "text/html"}}}
	d.event(ev("Fetch.requestPaused", "s1", page))
	if cont := f.last("Fetch.continueRequest"); cont == nil || cont.params["requestId"] != "r1" || len(caught) != 0 {
		t.Fatal("a page was not let through")
	}

	file := map[string]any{"requestId": "r2", "request": map[string]any{"url": "https://h/f.zip", "headers": map[string]any{"Cookie": "a=1", "Referer": "https://h/page"}},
		"responseStatusCode": 200, "responseHeaders": []any{
			map[string]any{"name": "Content-Type", "value": "application/zip"},
			map[string]any{"name": "Content-Disposition", "value": `attachment; filename="f.zip"`}}}
	d.event(ev("Fetch.requestPaused", "s1f", file))
	fail := f.last("Fetch.failRequest")
	if fail == nil || fail.session != "s1f" || fail.params["requestId"] != "r2" {
		t.Fatal("the download was not dropped")
	}
	if len(caught) != 1 || caught[0] != "p1" {
		t.Fatalf("caught %v, want the iframe's page p1", caught)
	}
	if c.URL != "https://h/f.zip" || c.Cookie != "a=1" || c.Referer != "https://h/page" || c.Headers["content-disposition"] == "" {
		t.Fatalf("catch %+v", c)
	}
}

func TestPageEventsOfOurContextsOnly(t *testing.T) {
	var log []string
	d, _ := driver(t, Events{
		Opened:  func(t, o, u string) { log = append(log, "open "+t+" by "+o+" "+u) },
		Loading: func(t, u string) { log = append(log, "loading "+t+" "+u) },
		Loaded:  func(t, u string) { log = append(log, "loaded "+t+" "+u) },
		Closed:  func(t string) { log = append(log, "closed "+t) },
	})
	ctx, _ := d.Context("eth", 4001)
	d.event(ev("Target.targetCreated", "", map[string]any{"targetInfo": map[string]any{"targetId": "p2", "type": "page", "openerId": "p1", "url": "https://ad", "browserContextId": ctx}}))
	d.event(ev("Target.targetCreated", "", map[string]any{"targetInfo": map[string]any{"targetId": "u", "type": "page", "browserContextId": "other"}}))
	d.event(ev("Target.attachedToTarget", "", map[string]any{"sessionId": "s2", "targetInfo": map[string]any{"targetId": "p2", "type": "page", "browserContextId": ctx}}))
	d.event(ev("Page.frameNavigated", "s2", map[string]any{"frame": map[string]any{"id": "F", "url": "https://ad/2"}}))
	d.event(ev("Page.frameNavigated", "s2", map[string]any{"frame": map[string]any{"id": "G", "parentId": "F", "url": "https://iframe"}}))
	d.event(ev("Page.loadEventFired", "s2", map[string]any{}))
	d.event(ev("Target.targetDestroyed", "", map[string]any{"targetId": "p2"}))
	want := "open p2 by p1 https://ad|loading p2 https://ad/2|loaded p2 https://ad/2|closed p2"
	if got := strings.Join(log, "|"); got != want {
		t.Fatalf("events %s\nwant   %s", got, want)
	}
	if d.session("p2") != "" {
		t.Fatal("a closed page kept its session")
	}
}

func TestThePageScriptGoesInOncePerDocumentAndTalksBothWays(t *testing.T) {
	var msgs []string
	d, f := driver(t, Events{Message: func(target, payload string) { msgs = append(msgs, target+" "+payload) }})
	was := pageScript
	t.Cleanup(func() { pageScript = was })
	pageScript = func() (string, error) { return prelude + "/*script*/", nil }
	ctx, _ := d.Context("eth", 4001)
	d.event(ev("Target.attachedToTarget", "", map[string]any{"sessionId": "s1", "targetInfo": map[string]any{"targetId": "p1", "type": "page", "browserContextId": ctx}}))
	inner := f.reply
	alive := true
	f.reply = func(m string, p map[string]any) (string, error) {
		switch m {
		case "Page.getFrameTree":
			return `{"frameTree":{"frame":{"id":"F1"}}}`, nil
		case "Page.createIsolatedWorld":
			return `{"executionContextId":7}`, nil
		case "Runtime.evaluate":
			if p["expression"] == "1" && !alive {
				return `{"exceptionDetails":{"text":"Cannot find context"}}`, nil
			}
		}
		return inner(m, p)
	}
	if err := d.Drive("p1"); err != nil {
		t.Fatal(err)
	}
	if w := f.last("Page.createIsolatedWorld"); w.params["frameId"] != "F1" || w.params["worldName"] != world {
		t.Fatalf("world %v", w.params)
	}
	// The binding after the world: it reaches only worlds that exist (Edge 157).
	if got := strings.Join(f.methods("s1"), ","); !strings.Contains(got, "Page.createIsolatedWorld,Runtime.addBinding,Runtime.evaluate") {
		t.Fatalf("order %s", got)
	}
	if b := f.last("Runtime.addBinding"); b.params["name"] != binding || b.params["executionContextName"] != world {
		t.Fatalf("binding %v", b.params)
	}
	if f.last("Runtime.enable") != nil {
		t.Fatal("Runtime.enable: sites can detect it")
	}
	if e := f.last("Runtime.evaluate"); e.params["contextId"] != float64(7) || !strings.Contains(e.params["expression"].(string), "/*script*/") {
		t.Fatalf("script evaluate %v", e.params)
	}
	d.Drive("p1") // same document
	if n := strings.Count(strings.Join(f.methods("s1"), ","), "Page.createIsolatedWorld"); n != 1 {
		t.Fatalf("script put in %d times into one document", n)
	}
	alive = false // the page navigated
	d.Drive("p1")
	if n := strings.Count(strings.Join(f.methods("s1"), ","), "Page.createIsolatedWorld"); n != 2 {
		t.Fatal("a new document did not get the script")
	}

	if err := d.Send("p1", map[string]any{"cmd": "replay", "index": 0}); err != nil {
		t.Fatal(err)
	}
	if e := f.last("Runtime.evaluate"); e.params["expression"] != `__dmsDeliver({"cmd":"replay","index":0})` {
		t.Fatalf("send %v", e.params["expression"])
	}
	d.event(ev("Runtime.bindingCalled", "s1", map[string]any{"name": binding, "payload": `{"type":"clicked"}`}))
	d.event(ev("Runtime.bindingCalled", "s1", map[string]any{"name": "somethingElse", "payload": "x"}))
	if len(msgs) != 1 || msgs[0] != `p1 {"type":"clicked"}` {
		t.Fatalf("messages %v", msgs)
	}
}
