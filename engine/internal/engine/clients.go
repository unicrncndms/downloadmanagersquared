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
			req.IfaceName, req.SocksPort = "VPN via "+e.ifaces[base].info.Name, st.SocksPort
		}
	} else if ent := e.ifaces[ifaceID]; ent != nil {
		req.IfaceName, req.SocksPort = ent.info.Name, ent.socks.Port()
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
// recording running for it.
func (e *Engine) canRecordLocked(s download.Summary, browser string) bool {
	if s.Kind != "" || s.Status == download.StatusCompleted || s.PageURL == "" {
		return false
	}
	if c := e.learning[s.ID]; c != nil && e.clients[c] != nil {
		return false
	}
	for k := range e.resolves {
		if strings.HasPrefix(k, s.ID+"|") {
			return false
		}
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

// startBrowser starts a browser so its extension connects; tests replace it.
var startBrowser = winutil.OpenBrowser

// extensionLocked is the most recently connected browser extension. Only
// OpenManager's fallback (WebView2 missing) still needs it.
func (e *Engine) extensionLocked() *ipc.Conn { return e.extensionForLocked("") }

// extensionForLocked is the most recently connected extension in the browser
// at exe ("" = in any browser).
func (e *Engine) extensionForLocked(exe string) *ipc.Conn {
	var best *ipc.Conn
	var seq int64 = -1
	for c, cl := range e.clients {
		if cl.extension && cl.seq > seq && (exe == "" || strings.EqualFold(cl.browser, exe)) {
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

// sendAll writes an encoded event to every client.
func (e *Engine) sendAll(b []byte) {
	e.mu.Lock()
	cs := make([]*ipc.Conn, 0, len(e.clients))
	for c := range e.clients {
		cs = append(cs, c)
	}
	e.mu.Unlock()
	for _, c := range cs {
		if err := c.WriteRaw(b); err != nil {
			// A client that takes nothing for 10 s would hold up every tick by
			// that long: it is hung up on, and connects again when it can.
			log.Printf("dropping a client that doesn't take events: %v", err)
			c.Close()
		}
	}
}
