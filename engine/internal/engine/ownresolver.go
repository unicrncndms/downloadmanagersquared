package engine

import (
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
	recipes, err := resolver.OpenRecipes(filepath.Join(e.dir, "recipes.json"))
	if err != nil {
		log.Printf("the engine's recipes can't be read (%v): starting without them", err)
		recipes, _ = resolver.OpenRecipes("")
	}
	profile := filepath.Join(e.dir, "resolver-profile")
	r := &resolver.Resolver{
		Recipes: recipes,
		Hooks:   e.ownHooks(),
		Start: func(ev resolver.Events) (*resolver.Driver, resolver.Browser, error) {
			return resolver.Start(exe, profile, ev)
		},
		Idle: 2 * time.Minute,
	}
	e.mu.Lock()
	e.own = r
	e.ownHosts = hostSet(recipes.Hosts())
	e.mu.Unlock()
}

func hostSet(hosts []string) map[string]bool {
	m := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		m[h] = true
	}
	return m
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
		RecipeHosts: func(hosts []string) {
			e.mu.Lock()
			e.ownHosts = hostSet(hosts)
			e.mu.Unlock()
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

// ownRequest is req for the engine's resolver, with the download's cookies
// as the login for the landing page.
func (e *Engine) ownRequest(req resolveReq) resolver.Request {
	r := resolver.Request{ID: req.ID, Iface: req.Iface, IfaceName: req.IfaceName, PageURL: req.PageURL, SocksPort: req.SocksPort, Learn: req.Learn}
	if d, err := e.get(req.ID); err == nil {
		st := d.State()
		r.Cookies = pageCookies(string(st.Cookie), st.URL, req.PageURL)
	}
	return r
}

// pageCookies are the cookies of a download's Cookie header, for the landing
// page's host, when the download and its page are on one site. The header is
// what the browser sent to the download's host: on another site they aren't
// the page's.
// shortcut: only the download's own header, not the browser's whole jar for
// the page; ask the Chrome extension for its cookies if logins go missing.
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
