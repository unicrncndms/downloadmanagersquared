// Package resolver gets a fresh download link per interface for downloads
// from a Chromium browser, in a Chromium the engine drives itself (ADR 0011):
// one browser context per interface, its traffic through that interface's
// SOCKS port and its cookies its own, like a Firefox container.
package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"downloadmanagersquared/engine/internal/cdp"
)

// caller is what the driver needs of a *cdp.Browser.
type caller interface {
	Call(ctx context.Context, sessionID, method string, params any) (json.RawMessage, error)
}

// Catch is a download response a page started, dropped before its body.
type Catch struct {
	URL, Cookie, Referer string
	Status               int
	Headers              map[string]string // names lower case
}

// Events are what the driver tells its user. Each runs on the browser's event
// goroutine, in order; none may block for long.
type Events struct {
	Opened  func(target, opener, url string) // a page in one of the driver's contexts; opener "" for one Open made
	URL     func(target, url string)         // a page's address changed
	Closed  func(target string)
	Caught  func(target string, c Catch)
	Message func(target, payload string) // from the page script (script.go)
}

// Bounds is a window's place on screen.
type Bounds struct{ Left, Top, Width, Height int }

// Driver runs the pages of resolves in one browser.
type Driver struct {
	b  caller
	ev Events

	mu       sync.Mutex
	contexts map[string]ctxEntry // iface -> its context
	ours     map[string]bool     // context ids the driver made
	sessions map[string]string   // session id -> the page target it belongs to (an iframe's: its page's)
	pages    map[string]string   // page target -> its session
	worlds   map[string]int      // page target -> the page script's execution context (script.go)
}

type ctxEntry struct {
	id   string
	port int
}

const callTimeout = 15 * time.Second

func newDriver(ev Events) *Driver {
	return &Driver{ev: ev, contexts: map[string]ctxEntry{}, ours: map[string]bool{}, sessions: map[string]string{}, pages: map[string]string{}, worlds: map[string]int{}}
}

// Start runs exe with its profile in dir and returns its driver. The browser
// ends with b.Close (see cdp.Keeper).
func Start(exe, dir string, ev Events) (*Driver, *cdp.Browser, error) {
	d := newDriver(ev)
	b, err := cdp.Start(exe, dir, d.event)
	if err != nil {
		return nil, nil, err
	}
	d.b = b // before init: no target event comes until init asks for them
	if err := d.init(); err != nil {
		b.Close()
		return nil, nil, err
	}
	return d, b, nil
}

func (d *Driver) call(session, method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return d.b.Call(ctx, session, method, params)
}

func (d *Driver) init() error {
	if _, err := d.call("", "Target.setDiscoverTargets", map[string]any{"discover": true}); err != nil {
		return err
	}
	_, err := d.call("", "Target.setAutoAttach", map[string]any{"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true})
	return err
}

// Context is iface's browser context, made on first use. A new socksPort
// (the interface's server was restarted) gets a new context: the old one's
// pages go with it.
func (d *Driver) Context(iface string, socksPort int) (string, error) {
	d.mu.Lock()
	old, ok := d.contexts[iface]
	d.mu.Unlock()
	if ok && old.port == socksPort {
		return old.id, nil
	}
	if ok {
		d.DropContext(iface)
	}
	r, err := d.call("", "Target.createBrowserContext", map[string]any{
		"disposeOnDetach": true,
		"proxyServer":     fmt.Sprintf("socks5://127.0.0.1:%d", socksPort),
		// Even loopback goes through the proxy: nothing leaves outside the interface.
		"proxyBypassList": "<-loopback>",
	})
	if err != nil {
		return "", err
	}
	var c struct{ BrowserContextID string }
	json.Unmarshal(r, &c)
	// Downloads are caught before this, never saved by the browser.
	if _, err := d.call("", "Browser.setDownloadBehavior", map[string]any{"behavior": "deny", "browserContextId": c.BrowserContextID}); err != nil {
		d.call("", "Target.disposeBrowserContext", map[string]any{"browserContextId": c.BrowserContextID})
		return "", err
	}
	d.mu.Lock()
	d.contexts[iface] = ctxEntry{c.BrowserContextID, socksPort}
	d.ours[c.BrowserContextID] = true
	d.mu.Unlock()
	return c.BrowserContextID, nil
}

// DropContext closes iface's context and its pages, if it has one.
func (d *Driver) DropContext(iface string) {
	d.mu.Lock()
	c, ok := d.contexts[iface]
	delete(d.contexts, iface)
	d.mu.Unlock()
	if ok {
		d.call("", "Target.disposeBrowserContext", map[string]any{"browserContextId": c.id})
		d.mu.Lock()
		delete(d.ours, c.id)
		d.mu.Unlock()
	}
}

// Open loads url in a new window of the context, minimized unless shown.
func (d *Driver) Open(contextID, url string, shown bool) (string, error) {
	state := "minimized"
	if shown {
		state = "normal"
	}
	r, err := d.call("", "Target.createTarget", map[string]any{"url": url, "browserContextId": contextID, "newWindow": true, "windowState": state})
	if err != nil {
		return "", err
	}
	var t struct{ TargetID string }
	json.Unmarshal(r, &t)
	return t.TargetID, nil
}

func (d *Driver) window(target string) (int, error) {
	r, err := d.call("", "Browser.getWindowForTarget", map[string]any{"targetId": target})
	if err != nil {
		return 0, err
	}
	var w struct{ WindowID int }
	json.Unmarshal(r, &w)
	return w.WindowID, nil
}

// Show puts target's window at b and in front.
func (d *Driver) Show(target string, b Bounds) error {
	w, err := d.window(target)
	if err != nil {
		return err
	}
	// A minimized window takes no bounds: restore it first.
	if _, err := d.call("", "Browser.setWindowBounds", map[string]any{"windowId": w, "bounds": map[string]any{"windowState": "normal"}}); err != nil {
		return err
	}
	if _, err := d.call("", "Browser.setWindowBounds", map[string]any{"windowId": w, "bounds": map[string]any{"left": b.Left, "top": b.Top, "width": b.Width, "height": b.Height}}); err != nil {
		return err
	}
	_, err = d.call("", "Target.activateTarget", map[string]any{"targetId": target})
	return err
}

// Minimize hides target's window.
func (d *Driver) Minimize(target string) error {
	w, err := d.window(target)
	if err != nil {
		return err
	}
	_, err = d.call("", "Browser.setWindowBounds", map[string]any{"windowId": w, "bounds": map[string]any{"windowState": "minimized"}})
	return err
}

// Close closes the page.
func (d *Driver) Close(target string) error {
	_, err := d.call("", "Target.closeTarget", map[string]any{"targetId": target})
	return err
}

// session is target's attached session, "" if it has none (yet).
func (d *Driver) session(target string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pages[target]
}

type targetInfo struct {
	TargetID         string `json:"targetId"`
	Type             string `json:"type"`
	URL              string `json:"url"`
	OpenerID         string `json:"openerId"`
	BrowserContextID string `json:"browserContextId"`
}

func (d *Driver) mine(t targetInfo) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ours[t.BrowserContextID]
}

func (d *Driver) event(e cdp.Event) {
	switch e.Method {
	case "Target.targetCreated", "Target.targetInfoChanged":
		var p struct{ TargetInfo targetInfo }
		json.Unmarshal(e.Params, &p)
		t := p.TargetInfo
		if t.Type != "page" || !d.mine(t) {
			return
		}
		if e.Method == "Target.targetCreated" {
			if d.ev.Opened != nil {
				d.ev.Opened(t.TargetID, t.OpenerID, t.URL)
			}
		} else if d.ev.URL != nil {
			d.ev.URL(t.TargetID, t.URL)
		}
	case "Target.targetDestroyed":
		var p struct{ TargetID string }
		json.Unmarshal(e.Params, &p)
		d.mu.Lock()
		s, ok := d.pages[p.TargetID]
		delete(d.pages, p.TargetID)
		delete(d.worlds, p.TargetID)
		for k, v := range d.sessions {
			if v == p.TargetID || k == s {
				delete(d.sessions, k)
			}
		}
		d.mu.Unlock()
		if ok && d.ev.Closed != nil {
			d.ev.Closed(p.TargetID)
		}
	case "Target.attachedToTarget":
		d.attached(e)
	case "Fetch.requestPaused":
		d.paused(e)
	case "Runtime.bindingCalled":
		d.bindingCalled(e)
	}
}

func (d *Driver) attached(e cdp.Event) {
	var p struct {
		SessionID  string     `json:"sessionId"`
		TargetInfo targetInfo `json:"targetInfo"`
	}
	json.Unmarshal(e.Params, &p)
	t := p.TargetInfo
	resume := func() { d.call(p.SessionID, "Runtime.runIfWaitingForDebugger", nil) }
	if (t.Type != "page" && t.Type != "iframe") || !d.mine(t) {
		// Not a resolve's: let it run, unwatched.
		resume()
		if e.SessionID == "" {
			d.call("", "Target.detachFromTarget", map[string]any{"sessionId": p.SessionID})
		}
		return
	}
	d.mu.Lock()
	page := t.TargetID
	if t.Type == "iframe" {
		page = d.sessions[e.SessionID] // attached through its page's session
	}
	d.sessions[p.SessionID] = page
	d.mu.Unlock()
	d.call(p.SessionID, "Fetch.enable", map[string]any{"patterns": []any{
		map[string]any{"resourceType": "Document", "requestStage": "Response"},
		map[string]any{"resourceType": "Other", "requestStage": "Response"},
	}})
	// Out-of-process iframes are attached to this session, waiting like their page.
	d.call(p.SessionID, "Target.setAutoAttach", map[string]any{"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true})
	if t.Type == "page" {
		d.mu.Lock()
		d.pages[page] = p.SessionID
		d.mu.Unlock()
	}
	resume()
}

func (d *Driver) paused(e cdp.Event) {
	var p struct {
		RequestID string `json:"requestId"`
		Request   struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"request"`
		Status  int `json:"responseStatusCode"`
		Headers []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"responseHeaders"`
		ErrorReason string `json:"responseErrorReason"`
	}
	json.Unmarshal(e.Params, &p)
	h := map[string]string{}
	for _, x := range p.Headers {
		h[strings.ToLower(x.Name)] = x.Value
	}
	if p.ErrorReason != "" || !isTheDownload(h, p.Status) {
		d.call(e.SessionID, "Fetch.continueRequest", map[string]any{"requestId": p.RequestID})
		return
	}
	d.call(e.SessionID, "Fetch.failRequest", map[string]any{"requestId": p.RequestID, "errorReason": "Aborted"})
	req := map[string]string{}
	for k, v := range p.Request.Headers {
		req[strings.ToLower(k)] = v
	}
	d.mu.Lock()
	page := d.sessions[e.SessionID]
	d.mu.Unlock()
	if d.ev.Caught != nil {
		d.ev.Caught(page, Catch{URL: p.Request.URL, Cookie: req["cookie"], Referer: req["referer"], Status: p.Status, Headers: h})
	}
}
