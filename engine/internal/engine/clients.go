package engine

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/ipc"
	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/winutil"
)

type client struct {
	extension bool
	noResolve bool   // said resolves: false (no proxy.onRequest or containers): never sent a resolve
	unwatch   bool   // said unwatch: gets a badge event, not the whole state
	seq       int64  // higher = connected later
	browser   string // the exe of the browser the extension runs in, from its hello
	userAgent string // that browser's, from its hello
	// The landing-page hosts that browser has a recipe for, from recipeHosts;
	// nil until the extension says.
	recipeHosts map[string]bool
}

type resolveReq struct {
	ID        string `json:"id"`
	Iface     string `json:"iface"`
	IfaceName string `json:"ifaceName"`
	PageURL   string `json:"pageUrl"`
	SocksPort int    `json:"socksPort"`
	Learn     bool   `json:"learn,omitempty"` // only to record the recipe: no link is reported
	// CookieStore is the browser's cookie store the download came from: its
	// cookies for the landing page are copied into the resolve's container.
	CookieStore string `json:"cookieStore,omitempty"`
	Browser     string `json:"-"` // only the extension in this browser gets it ("" = any)
}

// RequestResolve has the extension in the browser the download came from open
// its page through ifaceID: the page's clicks are learned in that browser, and
// its cookies are there. If that browser isn't running, it is started and
// gets the request once its extension connects.
func (e *Engine) RequestResolve(id, ifaceID, pageURL, browser string) {
	if browser != "" {
		if fi, err := os.Stat(browser); err != nil || fi.IsDir() {
			browser = "" // uninstalled since: any browser will do
		}
	}
	store := ""
	if d, err := e.get(id); err == nil {
		store = d.CookieStore()
	}
	e.mu.Lock()
	req := e.resolveReqLocked(id, ifaceID, pageURL, browser)
	req.CookieStore = store
	e.resolves[id+"|"+ifaceID] = req
	c := e.extensionForLocked(browser)
	start := c == nil && browser != "" && time.Since(e.started[strings.ToLower(browser)]) > time.Minute
	if start {
		e.started[strings.ToLower(browser)] = time.Now()
	}
	e.mu.Unlock()
	log.Printf("resolve %s via %s", id, req.IfaceName)
	switch {
	case c != nil:
		c.Emit("resolve", req)
	case start:
		if err := startBrowser(browser, ""); err != nil {
			log.Printf("starting the browser for a resolve: %v", err)
		}
	}
}

func (e *Engine) resolveReqLocked(id, ifaceID, pageURL, browser string) resolveReq {
	req := resolveReq{ID: id, Iface: ifaceID, PageURL: pageURL, Browser: browser}
	if base, tun := paths.IfaceOf(ifaceID); tun {
		if st, ok := e.tunnelStatus(base); ok && e.ifaces[base] != nil {
			req.IfaceName, req.SocksPort = "VPN via "+e.nameLocked(base, e.ifaces[base].info.Name), st.SocksPort
		}
	} else if ent := e.ifaces[ifaceID]; ent != nil {
		req.IfaceName, req.SocksPort = e.nameLocked(ifaceID, ent.info.Name), ent.socks.Port()
	}
	return req
}

// RecordRecipe has the browser a download came from record its landing
// page's recipe now, through the main interface, while the download keeps its
// link: a resolve marked learn, outside needResolve, so no link changes.
func (e *Engine) RecordRecipe(id string) error {
	d, err := e.get(id)
	if err != nil {
		return err
	}
	st := d.State()
	if st.PageURL == "" {
		return fmt.Errorf("%s has no landing page", st.Filename)
	}
	e.mu.Lock()
	c := e.extensionForLocked(st.Browser)
	if c == nil {
		e.mu.Unlock()
		return errors.New("the browser this download came from isn't running")
	}
	req := e.resolveReqLocked(id, paths.MainInterface(e.pathSnapshotLocked()), st.PageURL, st.Browser)
	req.CookieStore = st.CookieStore
	req.Learn = true
	e.learning[id] = c
	e.mu.Unlock()
	log.Printf("recording a recipe for %s via %s", id, req.IfaceName)
	c.Emit("resolve", req)
	return nil
}

// canRecordLocked: the Record recipe button. An unfinished HTTP download with a
// landing page whose browser's extension is connected, has said which hosts
// it has recipes for, and has none for this one; with no resolve or
// recording running for it. resolving is resolvingIDsLocked, built once per tick.
func (e *Engine) canRecordLocked(s download.Summary, browser string, resolving map[string]bool) bool {
	if s.Kind != "" || s.Status == download.StatusCompleted || s.PageURL == "" {
		return false
	}
	if c := e.learning[s.ID]; c != nil && e.clients[c] != nil {
		return false
	}
	if resolving[s.ID] {
		return false
	}
	for _, l := range s.Links {
		if l.Status == download.LinkResolving {
			return false
		}
	}
	c := e.extensionForLocked(browser)
	if c == nil {
		return false
	}
	hosts := e.clients[c].recipeHosts
	u, err := url.Parse(s.PageURL)
	return hosts != nil && err == nil && u.Hostname() != "" && !hosts[u.Hostname()]
}

// resolvingIDsLocked is the ids of the downloads with a resolve pending: the
// part of each e.resolves key before its "|".
func (e *Engine) resolvingIDsLocked() map[string]bool {
	ids := make(map[string]bool, len(e.resolves))
	for k := range e.resolves {
		id, _, _ := strings.Cut(k, "|")
		ids[id] = true
	}
	return ids
}

// startBrowser starts a browser so its extension connects; tests replace it.
var startBrowser = winutil.OpenBrowser

// extensionLocked is the most recently connected browser extension. Only
// OpenWindow's fallback (WebView2 missing) still needs it.
func (e *Engine) extensionLocked() *ipc.Conn { return e.pickLocked("", false) }

// extensionForLocked is the most recently connected extension that can run a
// resolve, in the browser at exe ("" = in any browser). Every caller sends a
// resolve (or needs one to be possible), so extensions that said they can't are skipped.
func (e *Engine) extensionForLocked(exe string) *ipc.Conn { return e.pickLocked(exe, true) }

// onlyNonResolvingLocked: some extension is connected and none can run a resolve.
func (e *Engine) onlyNonResolvingLocked() bool {
	return e.extensionLocked() != nil && e.extensionForLocked("") == nil
}

func (e *Engine) pickLocked(exe string, resolver bool) *ipc.Conn {
	var best *ipc.Conn
	var seq int64 = -1
	for c, cl := range e.clients {
		if cl.extension && cl.seq > seq && !(resolver && cl.noResolve) && (exe == "" || strings.EqualFold(cl.browser, exe)) {
			best, seq = c, cl.seq
		}
	}
	return best
}

func (e *Engine) broadcast(name string, data any) {
	if b, err := ipc.EncodeEvent(name, data); err == nil {
		e.sendAll(b)
	}
}

// audience is how many clients watch the list (every client does, until it
// says unwatch) and how many do not.
func (e *Engine) audience() (watchers, others int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, cl := range e.clients {
		if cl.unwatch {
			others++
		} else {
			watchers++
		}
	}
	return
}

// setWatching is the watch and unwatch methods. A client that starts to watch
// gets the current state at once.
func (e *Engine) setWatching(c *ipc.Conn, on bool) error {
	e.mu.Lock()
	cl := e.clients[c]
	if cl == nil {
		e.mu.Unlock()
		return nil
	}
	was := !cl.unwatch
	cl.unwatch = !on
	if !on {
		e.mu.Unlock()
		e.badgeStale.Store(true) // the client's badge is only as new as its last state
		return nil
	}
	defer e.mu.Unlock()
	if was {
		return nil
	}
	// Queued under the lock and in order: a push cannot slip a newer state in
	// before this older one, and the answer to watch follows it.
	b, err := ipc.EncodeEvent("state", e.snapshot)
	if err != nil {
		return err
	}
	return c.WriteRaw(b)
}

// sendAll queues an encoded event for every client.
func (e *Engine) sendAll(b []byte) { e.sendWhere(b, false, func(*client) bool { return true }) }

// sendWhere queues an encoded event for the clients keep accepts: in order
// with their other events, or, for latest (a picture of the state), replacing
// one they have not yet taken. The tick never waits on a client.
func (e *Engine) sendWhere(b []byte, latest bool, keep func(*client) bool) {
	e.mu.Lock()
	cs := make([]*ipc.Conn, 0, len(e.clients))
	for c, cl := range e.clients {
		if keep(cl) {
			cs = append(cs, c)
		}
	}
	e.mu.Unlock()
	for _, c := range cs {
		if latest {
			c.WriteLatest(b)
		} else {
			c.WriteRaw(b)
		}
	}
}
