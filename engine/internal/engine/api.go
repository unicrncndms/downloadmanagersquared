package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/ipc"
	"downloadmanagersquared/engine/internal/netif"
	"downloadmanagersquared/engine/internal/queue"
	"downloadmanagersquared/engine/internal/relver"
	"downloadmanagersquared/engine/internal/torrent"
	"downloadmanagersquared/engine/internal/winutil"
)

// ipc.Handler

func (e *Engine) Connected(c *ipc.Conn) {
	e.mu.Lock()
	e.clientSeq++
	e.clients[c] = &client{seq: e.clientSeq}
	e.mu.Unlock()
}

func (e *Engine) Disconnected(c *ipc.Conn) {
	e.mu.Lock()
	delete(e.clients, c)
	e.mu.Unlock()
}

type idParams struct {
	ID         string `json:"id"`
	Iface      string `json:"iface"`
	DeleteFile bool   `json:"deleteFile"`
}

type hashParams struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
	MD5    string `json:"md5"`
}

type chooseParams struct {
	ID    string  `json:"id"`
	Files []bool  `json:"files"`
	Dir   string  `json:"dir"`
	VPN   *string `json:"vpn"` // answers the VPN question too, when the files were known with it
}

type pageParams struct {
	ID      string `json:"id"`
	PageURL string `json:"pageUrl"`
}

type recipeHostsParams struct {
	Hosts []string `json:"hosts"` // landing-page hosts the extension's browser has a recipe for
}

type resolvedParams struct {
	ID      string `json:"id"`
	Iface   string `json:"iface"`
	URL     string `json:"url"`
	Cookie  string `json:"cookie"`
	Referer string `json:"referer"`
	Error   string `json:"error"`
}

// passwordParams carry an archive's password: never logged.
type passwordParams struct {
	ID       string `json:"id"`
	Password string `json:"password"`
}

type sftpLoginParams struct {
	Host string `json:"host"`
	User string `json:"user"`
}

type folderParams struct {
	ID   string  `json:"id"`
	On   *bool   `json:"on"`
	Into *string `json:"into"` // the folder to extract into, confirmed by the user
	Keep *string `json:"keep"` // archive keeping: download.KeepAuto, KeepFree, KeepUntil or KeepAfter
}

type helloParams struct {
	UserAgent string `json:"userAgent"` // the extension's navigator.userAgent
	Version   string `json:"version"`   // the extension's version
	Browser   string `json:"browser"`   // the browser's exe, added by dms-host
}

type hello struct {
	Settings  Settings   `json:"settings"`
	State     StateEvent `json:"state"`
	Autostart bool       `json:"autostart"`
	Version   string     `json:"version"`
	Clash     bool       `json:"clash"` // the extension's version and the engine's may not work together
}

func decode[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) == 0 {
		return v, nil
	}
	err := json.Unmarshal(raw, &v)
	return v, err
}

// A handler is one method of the protocol: it decodes its own params.
type handler func(e *Engine, c *ipc.Conn, raw json.RawMessage) (any, error)

// on decodes the params once and runs f.
func on[P any](f func(e *Engine, c *ipc.Conn, p P) (any, error)) handler {
	return func(e *Engine, c *ipc.Conn, raw json.RawMessage) (any, error) {
		p, err := decode[P](raw)
		if err != nil {
			return nil, err
		}
		return f(e, c, p)
	}
}

// with is on for a method that doesn't need the calling client.
func with[P any](f func(e *Engine, p P) (any, error)) handler {
	return on(func(e *Engine, _ *ipc.Conn, p P) (any, error) { return f(e, p) })
}

// ack is with for a method that answers nothing but whether it worked.
func ack[P any](f func(e *Engine, p P) error) handler {
	return with(func(e *Engine, p P) (any, error) { return nil, f(e, p) })
}

// bare is a method with no params.
func bare(f func(e *Engine) (any, error)) handler {
	return func(e *Engine, _ *ipc.Conn, _ json.RawMessage) (any, error) { return f(e) }
}

// Params of the methods that have no struct of their own above.
type (
	limitParams struct {
		ID    string `json:"id"`
		Limit int64  `json:"limit"` // bytes per second, 0 = none
	}
	moveParams struct {
		ID     string `json:"id"`
		Before string `json:"before"` // "" = last
	}
	vpnParams struct {
		ID  string `json:"id"`
		VPN string `json:"vpn"`
	}
	dirParams struct {
		ID  string `json:"id"`
		Dir string `json:"dir"`
	}
	startParams struct {
		Start string `json:"start"`
	}
	importParams struct {
		Iface string `json:"iface"`
		Conf  string `json:"conf"`
	}
	piaParams struct {
		User   string  `json:"user"`
		Pass   string  `json:"pass"`
		Region *string `json:"region"`
	}
	portParams struct {
		Iface string `json:"iface"`
		Port  int    `json:"port"`
	}
	ifaceParams struct {
		Iface string `json:"iface"`
	}
	onParams struct {
		On bool `json:"on"`
	}
	hashParam struct {
		Hash string `json:"hash"`
	}
)

// jobCall is ack for a method on one download or torrent: an id that is in no
// row fails before f is asked.
func jobCall(f func(e *Engine, j queue.Entry, p idParams) (any, error)) handler {
	return with(func(e *Engine, p idParams) (any, error) {
		j, err := e.queue.Get(p.ID)
		if err != nil {
			return nil, err
		}
		return f(e, j, p)
	})
}

// getTorrent is the torrent with this id.
func (e *Engine) getTorrent(id string) (*torrent.Torrent, error) {
	j, err := e.queue.Get(id)
	if err != nil {
		return nil, err
	}
	t, ok := j.(*torrent.Torrent)
	if !ok {
		return nil, fmt.Errorf("%q is not a torrent", id)
	}
	return t, nil
}

// methods is the whole protocol: every method a client may call, and what it
// does. The extension's calls are checked against these names by
// extension/test/protocol.test.js, which reads the keys from this file.
var methods = map[string]handler{
	"hello":    on(func(e *Engine, c *ipc.Conn, p helloParams) (any, error) { return e.hello(c, p), nil }),
	"getState": bare(func(e *Engine) (any, error) { return e.Snapshot(), nil }),
	// The exes of the browsers whose extension has said hello; setup ticks them.
	"extensionBrowsers": bare(func(e *Engine) (any, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		out := []string{}
		for _, cl := range e.clients {
			if cl.extension && cl.browser != "" {
				out = append(out, cl.browser)
			}
		}
		return out, nil
	}),
	"add": on(func(e *Engine, c *ipc.Conn, p AddRequest) (any, error) {
		e.mu.Lock()
		if cl := e.clients[c]; cl != nil { // from an extension: its browser's
			p.Browser = cl.browser
			if p.UserAgent == "" {
				p.UserAgent = cl.userAgent
			}
		}
		e.mu.Unlock()
		if p.CheckDup {
			id, known, err := e.AddUnlessKnown(p)
			if known {
				return map[string]any{"id": id, "duplicate": true}, nil
			}
			return map[string]string{"id": id}, err
		}
		id, err := e.Add(p)
		return map[string]string{"id": id}, err
	}),
	"startNow": ack(func(e *Engine, p idParams) error { return e.queue.StartNow(p.ID) }),
	"pause":    jobCall(func(e *Engine, _ queue.Entry, p idParams) (any, error) { return nil, e.queue.Pause(p.ID) }),
	"resume":   jobCall(func(e *Engine, _ queue.Entry, p idParams) (any, error) { return nil, e.queue.Resume(p.ID) }),
	"remove":   jobCall(func(e *Engine, _ queue.Entry, p idParams) (any, error) { return nil, e.Remove(p.ID, p.DeleteFile) }),
	"retryLink": jobCall(func(e *Engine, _ queue.Entry, p idParams) (any, error) {
		d, err := e.get(p.ID)
		if err != nil {
			return nil, err
		}
		d.RetryLink(p.Iface)
		return nil, nil
	}),
	"showInFolder": jobCall(func(e *Engine, j queue.Entry, _ idParams) (any, error) { return nil, winutil.ShowInFolder(j.Path()) }),
	"open": jobCall(func(e *Engine, j queue.Entry, _ idParams) (any, error) {
		if !j.Summary().Actions.Open {
			return nil, errors.New("only a finished download can be opened")
		}
		return nil, openFile(j.Path())
	}),
	"deleteFile": jobCall(func(e *Engine, j queue.Entry, p idParams) (any, error) {
		if !j.Summary().Actions.DeleteFile {
			return nil, errors.New("only a finished download's file can be deleted")
		}
		if err := recycleFile(j.Path()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, e.Remove(p.ID, false) // the file is gone: so is its row
	}),
	"again": jobCall(func(e *Engine, j queue.Entry, _ idParams) (any, error) {
		s := j.Summary()
		if !s.Actions.Again {
			return nil, errors.New("only a finished download can be downloaded again")
		}
		id, err := e.Add(AddRequest{URL: s.URL, PageURL: s.PageURL})
		return map[string]string{"id": id}, err
	}),
	"setLimit": ack(func(e *Engine, p limitParams) error {
		d, err := e.get(p.ID)
		if err != nil {
			return err
		}
		d.SetLimit(p.Limit)
		return nil
	}),
	// The next tick starts by the new order.
	"move": ack(func(e *Engine, p moveParams) error { return e.queue.Move(p.ID, p.Before) }),
	"chooseFiles": ack(func(e *Engine, p chooseParams) error {
		t, err := e.getTorrent(p.ID)
		if err != nil {
			return err
		}
		if p.VPN != nil && t.State().AskVPN {
			if t, err = e.chooseTorrentVPN(p.ID, *p.VPN); err != nil {
				return err
			}
		}
		dir, err := cleanDir(p.Dir)
		if err != nil {
			return err
		}
		return t.Choose(p.Files, dir)
	}),
	"setTorrentFiles": ack(func(e *Engine, p chooseParams) error {
		t, err := e.getTorrent(p.ID)
		if err != nil {
			return err
		}
		return t.SetFiles(p.Files)
	}),
	"torrentFiles": with(func(e *Engine, p idParams) (any, error) {
		j, err := e.queue.Get(p.ID)
		if err != nil {
			return nil, err
		}
		var files []download.FileSummary
		if t, ok := j.(*torrent.Torrent); ok {
			files = t.Files()
		}
		if files == nil {
			files = []download.FileSummary{}
		}
		return files, nil
	}),
	"downloadLog": with(func(e *Engine, p idParams) (any, error) {
		j, err := e.queue.Get(p.ID)
		if err != nil {
			return nil, err
		}
		if d, ok := j.(*download.Download); ok {
			return d.Log(), nil
		}
		return []download.Event{}, nil // torrents keep no log
	}),
	"chooseVpn": ack(func(e *Engine, p vpnParams) error {
		_, err := e.chooseTorrentVPN(p.ID, p.VPN)
		return err
	}),
	"pauseAll":  bare(func(e *Engine) (any, error) { e.PauseAll(); return nil, nil }),
	"resumeAll": bare(func(e *Engine) (any, error) { e.ResumeAll(); return nil, nil }),
	"setHashes": ack(func(e *Engine, p hashParams) error {
		d, err := e.get(p.ID)
		if err != nil {
			return err
		}
		return d.SetHashes(p.SHA256, p.MD5)
	}),
	"retryExtraction": ack(func(e *Engine, p idParams) error { return e.RetryExtraction(p.ID) }),
	"setFolder":       ack(func(e *Engine, p folderParams) error { return e.SetFolder(p.ID, p.On, p.Into, p.Keep) }),
	"trustHostKey": ack(func(e *Engine, p idParams) error {
		d, err := e.get(p.ID)
		if err != nil {
			return err
		}
		return d.TrustHostKey()
	}),
	"sftpLogins": bare(func(e *Engine) (any, error) { return download.SFTP.Logins(), nil }),
	"removeSftpLogin": ack(func(e *Engine, p sftpLoginParams) error { return download.SFTP.RemoveLogin(p.Host, p.User) }),
	"setArchivePassword": ack(func(e *Engine, p passwordParams) error {
		d, err := e.get(p.ID)
		if err != nil {
			return err
		}
		return d.SetArchivePassword(p.Password)
	}),
	"pickFolder":     ack(func(e *Engine, p idParams) error { return e.PickFolder(p.ID) }),
	"pickSaveFolder": bare(func(e *Engine) (any, error) { e.PickSaveFolder(); return nil, nil }),
	"setDir":         ack(func(e *Engine, p dirParams) error { return e.SetDir(p.ID, p.Dir) }),
	"pickDir":        ack(func(e *Engine, p idParams) error { return e.PickDir(p.ID) }),
	"browseFolder": with(func(e *Engine, p startParams) (any, error) {
		return map[string]string{"dir": e.BrowseFolder(p.Start)}, nil
	}),
	"setPageUrl": ack(func(e *Engine, p pageParams) error {
		d, err := e.get(p.ID)
		if err != nil {
			return err
		}
		d.SetPageURL(p.PageURL)
		return nil
	}),
	"checkLines":   bare(func(e *Engine) (any, error) { return e.CheckLines(netif.StunServer), nil }),
	"recordRecipe": ack(func(e *Engine, p idParams) error { return e.RecordRecipe(p.ID) }),
	"learnEnded": ack(func(e *Engine, p idParams) error {
		e.mu.Lock()
		delete(e.learning, p.ID)
		e.mu.Unlock()
		return nil
	}),
	"recipeHosts": on(func(e *Engine, c *ipc.Conn, p recipeHostsParams) (any, error) {
		hosts := make(map[string]bool, len(p.Hosts))
		for _, h := range p.Hosts {
			hosts[h] = true
		}
		e.mu.Lock()
		if cl := e.clients[c]; cl != nil {
			cl.recipeHosts = hosts
		}
		e.mu.Unlock()
		return nil, nil
	}),
	"resolved": ack(func(e *Engine, p resolvedParams) error {
		d, err := e.takeResolve(p)
		if err != nil {
			return err
		}
		log.Printf("resolved %s via %s", p.ID, p.Iface)
		d.SetResolved(p.Iface, p.URL, p.Cookie, p.Referer)
		return nil
	}),
	"resolveFailed": ack(func(e *Engine, p resolvedParams) error {
		d, err := e.takeResolve(p)
		if err != nil {
			return err
		}
		log.Printf("resolve failed %s via %s: %s", p.ID, p.Iface, p.Error)
		d.ResolveFailed(p.Iface, p.Error)
		return nil
	}),
	"getSettings": bare(func(e *Engine) (any, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.settings.clone(), nil
	}),
	"setSettings": with(func(e *Engine, p settingsPatch) (any, error) {
		if p.SaveDir != nil {
			if err := checkSaveDir(*p.SaveDir); err != nil {
				return nil, err
			}
		}
		return e.setSettings(p), nil
	}),
	"restoreSettings": bare(func(e *Engine) (any, error) { return e.restoreSettings(), nil }),
	"openLog":         bare(func(e *Engine) (any, error) { return nil, openFile(filepath.Join(e.dir, "engine.log")) }),
	"openDataFolder":  bare(func(e *Engine) (any, error) { return nil, openFile(e.dir) }),
	"setInterface":    with(func(e *Engine, p ifacePatch) (any, error) { return e.setInterface(p), nil }),
	"importTunnel": ack(func(e *Engine, p importParams) error {
		if p.Iface == "" {
			return errors.New("no connection to carry the tunnel")
		}
		return e.vpn.Import(p.Iface, p.Conf)
	}),
	"setPia": ack(func(e *Engine, p piaParams) error {
		if p.Region != nil {
			e.applySettings(func(s *Settings) { s.PIARegion = *p.Region })
			e.vpn.SetPIARegion(*p.Region)
		}
		if p.User != "" {
			return e.vpn.SetPIA(strings.TrimSpace(p.User), p.Pass)
		}
		return nil
	}),
	"clearPia":         bare(func(e *Engine) (any, error) { return nil, e.vpn.SetPIA("", "") }),
	"piaRegions":       bare(func(e *Engine) (any, error) { return e.piaRegions() }),
	"setForwardedPort": ack(func(e *Engine, p portParams) error { return e.setForwardedPort(p.Iface, p.Port) }),
	"removeTunnel":     ack(func(e *Engine, p ifaceParams) error { return e.removeTunnel(p.Iface) }),
	"forgetInterface":  ack(func(e *Engine, p idParams) error { return e.forgetInterface(p.ID) }),
	"refreshInterfaces": bare(func(e *Engine) (any, error) {
		e.refreshInterfaces()
		return nil, nil
	}),
	"getAutostart": bare(func(e *Engine) (any, error) { return winutil.Autostart(), nil }),
	"version":      bare(func(e *Engine) (any, error) { return Version, nil }),
	"setAutostart": ack(func(e *Engine, p onParams) error { return winutil.SetAutostart(p.On) }),
	// An update asks before it has the engine quit.
	"restartCost": func(e *Engine, c *ipc.Conn, _ json.RawMessage) (any, error) {
		if e.fromExtension(c) {
			return nil, errNotForExtension
		}
		return e.RestartCost(), nil
	},
	"quit": func(e *Engine, c *ipc.Conn, _ json.RawMessage) (any, error) {
		if e.fromExtension(c) {
			return nil, errNotForExtension
		}
		// The caller waits for this process to exit before replacing or removing it.
		e.reqOnce.Do(func() { close(e.quitReq) })
		return map[string]int{"pid": os.Getpid()}, nil
	},
	// The name from before the Downloads window: an upgrade still calls the
	// old engine with it. (The engine also sends an openManager event, to the
	// extension, when the window can't open; two directions, one name.) Can go
	// one release after 0.x, when every engine in use has the window.
	"openManager": func(e *Engine, _ *ipc.Conn, raw json.RawMessage) (any, error) {
		p, _ := decode[hashParam](raw)
		e.OpenWindow(p.Hash)
		return nil, nil
	},
}

// errNotForExtension answers the methods only the installer and the tray's
// own windows have a use for (docs/adr/0009).
var errNotForExtension = errors.New("not for the browser extension")

// fromExtension is whether c has said hello as the browser extension.
func (e *Engine) fromExtension(c *ipc.Conn) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	cl := e.clients[c]
	return cl != nil && cl.extension
}

// takeResolve forgets the resolve an extension answers and finds its download.
func (e *Engine) takeResolve(p resolvedParams) (*download.Download, error) {
	e.mu.Lock()
	delete(e.resolves, p.ID+"|"+p.Iface)
	e.mu.Unlock()
	return e.get(p.ID)
}

// Call runs one method of the protocol for client c.
func (e *Engine) Call(c *ipc.Conn, method string, raw json.RawMessage) (any, error) {
	select {
	case <-e.quit:
		// Shutdown is saving and closing the parts a call would use.
		return nil, errors.New("the engine is shutting down")
	default:
	}
	h, ok := methods[method]
	if !ok {
		return nil, fmt.Errorf("unknown method %q", method)
	}
	return h(e, c, raw)
}

// hello registers the caller as the browser extension and hands it anything it
// missed. The browser's user agent is kept for downloads added without one.
// The browser's exe is kept to open pages in that browser.
func (e *Engine) hello(c *ipc.Conn, p helloParams) hello {
	e.mu.Lock()
	if cl := e.clients[c]; cl != nil {
		cl.extension, cl.browser, cl.userAgent = true, p.Browser, p.UserAgent
	}
	if p.UserAgent != "" {
		e.userAgent = p.UserAgent
	}
	if p.Browser != "" {
		e.browser = p.Browser
	}
	pending := make([]resolveReq, 0, len(e.resolves))
	for _, r := range e.resolves {
		if r.Browser == "" || strings.EqualFold(r.Browser, p.Browser) {
			pending = append(pending, r)
		}
	}
	h := hello{Settings: e.settings.clone(), State: e.snapshot, Version: Version, Clash: relver.Clash(Version, p.Version)}
	e.mu.Unlock()
	if h.Clash {
		log.Printf("the extension is v%s but the engine is v%s: they may not work together", p.Version, Version)
	}
	h.Autostart = winutil.Autostart()

	go func() {
		for _, r := range pending {
			c.Emit("resolve", r)
		}
	}()
	return h
}
