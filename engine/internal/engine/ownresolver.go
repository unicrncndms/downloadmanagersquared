package engine

import (
	"encoding/json"
	"log"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"downloadmanagersquared/engine/internal/cdp"
	"downloadmanagersquared/engine/internal/resolver"
	"downloadmanagersquared/engine/internal/winutil"
)

// The engine's own resolver (docs/adr/0011): a Chromium the engine drives
// resolves the downloads of Chromium browsers, whose extension can't, and any
// download while no extension that can resolve is connected.

// ownResolver is what the engine needs of a *resolver.Resolver; tests fake it.
type ownResolver interface {
	Request(resolver.Request)
	Cancel(id string) // the download is gone: its pages close, nothing is reported
	Close()
}

// chromiumExe is the Chromium the engine drives, "" for none; tests replace it.
var chromiumExe = func() string { return cdp.Exe(winutil.Browsers()) }

// startOwnResolver sets up the engine's resolver if a Chromium is installed.
// Its browser starts with the first resolve.
func (e *Engine) startOwnResolver() {
	exe := chromiumExe()
	if exe == "" {
		log.Printf("no Chrome or Edge: only a Firefox-based browser can resolve")
		return
	}
	profile := filepath.Join(e.dir, "resolver-profile")
	r := &resolver.Resolver{
		Recipes: e.recipes,
		Hooks:   e.ownHooks(),
		Start: func(ev resolver.Events) (*resolver.Driver, resolver.Browser, error) {
			return resolver.Start(exe, profile, ev)
		},
		Idle: 2 * time.Minute,
	}
	e.mu.Lock()
	e.own = r
	e.mu.Unlock()
}

func (e *Engine) ownHooks() resolver.Hooks {
	return resolver.Hooks{
		Resolved: func(id, iface, u, cookie, referer string) {
			d, err := e.takeResolve(resolvedParams{ID: id, Iface: iface})
			if err != nil {
				return
			}
			log.Printf("resolved %s via %s in the engine's browser", id, iface)
			d.SetResolved(iface, u, cookie, referer)
		},
		Failed: func(id, iface, why string) {
			d, err := e.takeResolve(resolvedParams{ID: id, Iface: iface})
			if err != nil {
				return
			}
			log.Printf("resolve failed %s via %s in the engine's browser: %s", id, iface, why)
			d.ResolveFailed(iface, why)
		},
		LearnEnded: func(id, _ string) {
			e.mu.Lock()
			delete(e.ownLearning, id)
			e.mu.Unlock()
		},
		SetPageURL: func(id, page string) {
			if d, err := e.get(id); err == nil {
				d.SetPageURL(page)
			}
		},
		RecipeHosts: func([]string) { e.recipesChanged() },
		// Where the first recorded click was is where that browser's next
		// download from the host starts (its capture keeps the step-backs).
		LearnStepBack: func(id, start string) {
			d, err := e.get(id)
			if err != nil {
				return
			}
			e.mu.Lock()
			c := e.pickLocked(d.State().Browser, false)
			e.mu.Unlock()
			if c != nil {
				c.Emit("stepBack", map[string]string{"id": id, "url": start})
			}
		},
		Notify: func(title, msg string) { notify(title, msg, nil) },
	}
}

// ownsLocked: a resolve for a download from browser goes to the engine's
// resolver: there is one, no extension in that browser can resolve, and the
// browser is a Chromium one or unknown. A Firefox that isn't running is
// started instead: it has the recipes and the login.
func (e *Engine) ownsLocked(browser string) bool {
	return e.own != nil && e.extensionForLocked(browser) == nil && (browser == "" || winutil.IsChromium(browser))
}

// pageInfo is what the download's browser knows for a resolve in the
// engine's: its cookies for the landing page (the login), the download's page
// history (the record bar's way back) and the accent (the record bar's colours).
type pageInfo struct {
	ID      string            `json:"id"`
	Cookies []resolver.Cookie `json:"cookies"`
	Pages   []string          `json:"pages"`
	Tokens  json.RawMessage   `json:"tokens"`
	Error   string            `json:"error,omitempty"` // the browser couldn't say: why
}

// pageInfoWait is how long a resolve waits for the browser's pageInfo.
var pageInfoWait = 2 * time.Second

// startOwn runs req in the engine's resolver once the download's browser has
// said its pageInfo, or without it after pageInfoWait (the browser is closed,
// or its extension too old to answer): then with the download's own cookies.
func (e *Engine) startOwn(own ownResolver, req resolveReq) {
	r := resolver.Request{ID: req.ID, Iface: req.Iface, IfaceName: req.IfaceName, PageURL: req.PageURL, SocksPort: req.SocksPort, Learn: req.Learn}
	d, err := e.get(req.ID)
	if err != nil {
		return
	}
	st := d.State()
	r.From = browserName(st.Browser)
	if info, ok := e.askPageInfo(st.Browser, req.ID, req.PageURL, st.CookieStore); ok {
		r.Cookies, r.Pages = info.Cookies, info.Pages
		if len(info.Tokens) > 0 && string(info.Tokens) != "null" {
			r.Tokens = info.Tokens
		}
	} else {
		r.Cookies = pageCookies(string(st.Cookie), st.URL, req.PageURL)
	}
	own.Request(r)
}

// askPageInfo asks the extension in browser for download id's pageInfo and
// waits for it, at most pageInfoWait.
func (e *Engine) askPageInfo(browser, id, pageURL, store string) (pageInfo, bool) {
	ch := make(chan pageInfo, 1)
	e.mu.Lock()
	c := e.pickLocked(browser, false) // Chrome's extension, which can't resolve, answers this
	if c != nil {
		e.pageInfos[id] = append(e.pageInfos[id], ch)
	}
	e.mu.Unlock()
	if c == nil {
		return pageInfo{}, false
	}
	defer func() {
		e.mu.Lock()
		waiting := e.pageInfos[id]
		for i, w := range waiting {
			if w == ch {
				waiting = append(waiting[:i:i], waiting[i+1:]...)
				break
			}
		}
		if len(waiting) == 0 {
			delete(e.pageInfos, id)
		} else {
			e.pageInfos[id] = waiting
		}
		e.mu.Unlock()
	}()
	c.Emit("pageInfo", map[string]string{"id": id, "pageUrl": pageURL, "cookieStore": store})
	select {
	case info := <-ch:
		if info.Error != "" {
			log.Printf("no page info for %s from its browser (%s): resolving with the download's cookies", id, info.Error)
			return pageInfo{}, false
		}
		return info, true
	case <-time.After(pageInfoWait):
		log.Printf("no page info for %s from its browser: resolving with the download's cookies", id)
		return pageInfo{}, false
	}
}

// gotPageInfo is the pageInfo method: the answer to askPageInfo.
func (e *Engine) gotPageInfo(p pageInfo) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ch := range e.pageInfos[p.ID] {
		select {
		case ch <- p:
		default:
		}
	}
}

// pageCookies are the cookies of a download's Cookie header, for the landing
// page's host, when the download and its page are on one site. The header is
// what the browser sent to the download's host: on another site they aren't
// the page's. For when its browser can't say its own (askPageInfo).
func pageCookies(header, downloadURL, pageURL string) []resolver.Cookie {
	p, err1 := url.Parse(pageURL)
	dl, err2 := url.Parse(downloadURL)
	if header == "" || err1 != nil || err2 != nil || p.Hostname() == "" || siteOf(p.Hostname()) != siteOf(dl.Hostname()) {
		return nil
	}
	var out []resolver.Cookie
	for _, part := range strings.Split(header, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && name != "" {
			out = append(out, resolver.Cookie{Name: name, Value: value, Domain: p.Hostname(), Path: "/", Secure: p.Scheme == "https"})
		}
	}
	return out
}

// siteOf: rough "same site", the last two labels of the host.
func siteOf(host string) string {
	labels := strings.Split(host, ".")
	return strings.Join(labels[max(0, len(labels)-2):], ".")
}
