package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/ipc"
	"downloadmanagersquared/engine/internal/netif"
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

type folderParams struct {
	ID   string  `json:"id"`
	On   *bool   `json:"on"`
	Into *string `json:"into"` // the folder to extract into, confirmed by the user
}

type helloParams struct {
	UserAgent string `json:"userAgent"` // the extension's navigator.userAgent
	Browser   string `json:"browser"`   // the browser's exe, added by dms-host
}

type hello struct {
	Settings  Settings   `json:"settings"`
	State     StateEvent `json:"state"`
	Autostart bool       `json:"autostart"`
	Version   string     `json:"version"`
}

func decode[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) == 0 {
		return v, nil
	}
	err := json.Unmarshal(raw, &v)
	return v, err
}

func (e *Engine) Call(c *ipc.Conn, method string, raw json.RawMessage) (any, error) {
	switch method {
	case "hello":
		p, err := decode[helloParams](raw)
		if err != nil {
			return nil, err
		}
		return e.hello(c, p), nil
	case "getState":
		return e.Snapshot(), nil
	case "add":
		p, err := decode[AddRequest](raw)
		if err != nil {
			return nil, err
		}
		e.mu.Lock()
		if cl := e.clients[c]; cl != nil { // from an extension: its browser's
			p.Browser = cl.browser
			if p.UserAgent == "" {
				p.UserAgent = cl.userAgent
			}
		}
		e.mu.Unlock()
		id, err := e.Add(p)
		return map[string]string{"id": id}, err
	case "pause", "resume", "remove", "retryLink", "showInFolder":
		p, err := decode[idParams](raw)
		if err != nil {
			return nil, err
		}
		j, err := e.queue.Get(p.ID)
		if err != nil {
			return nil, err
		}
		switch method {
		case "pause":
			return nil, e.queue.Pause(p.ID)
		case "resume":
			return nil, e.queue.Resume(p.ID)
		case "remove":
			return nil, e.Remove(p.ID, p.DeleteFile)
		case "retryLink":
			d, err := e.get(p.ID)
			if err != nil {
				return nil, err
			}
			d.RetryLink(p.Iface)
		case "showInFolder":
			return nil, winutil.ShowInFolder(j.Path())
		}
		return nil, nil
	case "setLimit":
		p, err := decode[struct {
			ID    string `json:"id"`
			Limit int64  `json:"limit"` // bytes per second, 0 = none
		}](raw)
		if err != nil {
			return nil, err
		}
		d, err := e.get(p.ID)
		if err != nil {
			return nil, err
		}
		d.SetLimit(p.Limit)
		return nil, nil
	case "move":
		p, err := decode[struct {
			ID     string `json:"id"`
			Before string `json:"before"` // "" = last
		}](raw)
		if err != nil {
			return nil, err
		}
		return nil, e.queue.Move(p.ID, p.Before) // the next tick starts by the new order
	case "chooseFiles":
		p, err := decode[chooseParams](raw)
		if err != nil {
			return nil, err
		}
		j, err := e.queue.Get(p.ID)
		if err != nil {
			return nil, err
		}
		t, ok := j.(*torrent.Torrent)
		if !ok {
			return nil, fmt.Errorf("%q is not a torrent", p.ID)
		}
		if p.VPN != nil && t.State().AskVPN {
			if t, err = e.chooseTorrentVPN(p.ID, *p.VPN); err != nil {
				return nil, err
			}
		}
		return nil, t.Choose(p.Files, strings.TrimSpace(p.Dir))
	case "torrentFiles":
		p, err := decode[idParams](raw)
		if err != nil {
			return nil, err
		}
		j, err := e.queue.Get(p.ID)
		if err != nil {
			return nil, err
		}
		files := j.Summary().Files
		if files == nil {
			files = []download.FileSummary{}
		}
		return files, nil
	case "downloadLog":
		p, err := decode[idParams](raw)
		if err != nil {
			return nil, err
		}
		j, err := e.queue.Get(p.ID)
		if err != nil {
			return nil, err
		}
		if d, ok := j.(*download.Download); ok {
			return d.Log(), nil
		}
		return []download.Event{}, nil // torrents keep no log
	case "chooseVpn":
		p, err := decode[struct {
			ID  string `json:"id"`
			VPN string `json:"vpn"`
		}](raw)
		if err != nil {
			return nil, err
		}
		_, err = e.chooseTorrentVPN(p.ID, p.VPN)
		return nil, err
	case "pauseAll":
		e.PauseAll()
		return nil, nil
	case "resumeAll":
		e.ResumeAll()
		return nil, nil
	case "setHashes":
		p, err := decode[hashParams](raw)
		if err != nil {
			return nil, err
		}
		d, err := e.get(p.ID)
		if err != nil {
			return nil, err
		}
		return nil, d.SetHashes(p.SHA256, p.MD5)
	case "setFolder":
		p, err := decode[folderParams](raw)
		if err != nil {
			return nil, err
		}
		return nil, e.SetFolder(p.ID, p.On, p.Into)
	case "setArchivePassword":
		p, err := decode[passwordParams](raw)
		if err != nil {
			return nil, err
		}
		d, err := e.get(p.ID)
		if err != nil {
			return nil, err
		}
		return nil, d.SetArchivePassword(p.Password)
	case "pickFolder":
		p, err := decode[idParams](raw)
		if err != nil {
			return nil, err
		}
		return nil, e.PickFolder(p.ID)
	case "setPageUrl":
		p, err := decode[pageParams](raw)
		if err != nil {
			return nil, err
		}
		d, err := e.get(p.ID)
		if err != nil {
			return nil, err
		}
		d.SetPageURL(p.PageURL)
		return nil, nil
	case "checkLines":
		return e.CheckLines(netif.StunServer), nil
	case "recordRecipe":
		p, err := decode[idParams](raw)
		if err != nil {
			return nil, err
		}
		return nil, e.RecordRecipe(p.ID)
	case "learnEnded":
		p, err := decode[idParams](raw)
		if err != nil {
			return nil, err
		}
		e.mu.Lock()
		delete(e.learning, p.ID)
		e.mu.Unlock()
		return nil, nil
	case "recipeHosts":
		p, err := decode[recipeHostsParams](raw)
		if err != nil {
			return nil, err
		}
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
	case "resolved", "resolveFailed":
		p, err := decode[resolvedParams](raw)
		if err != nil {
			return nil, err
		}
		e.mu.Lock()
		delete(e.resolves, p.ID+"|"+p.Iface)
		e.mu.Unlock()
		d, err := e.get(p.ID)
		if err != nil {
			return nil, err
		}
		if method == "resolved" {
			log.Printf("resolved %s via %s", p.ID, p.Iface)
			d.SetResolved(p.Iface, p.URL, p.Cookie, p.Referer)
		} else {
			log.Printf("resolve failed %s via %s: %s", p.ID, p.Iface, p.Error)
			d.ResolveFailed(p.Iface, p.Error)
		}
		return nil, nil
	case "getSettings":
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.settings.clone(), nil
	case "setSettings":
		p, err := decode[settingsPatch](raw)
		if err != nil {
			return nil, err
		}
		return e.setSettings(p), nil
	case "setInterface":
		p, err := decode[ifacePatch](raw)
		if err != nil {
			return nil, err
		}
		return e.setInterface(p), nil
	case "importTunnel":
		p, err := decode[struct {
			Iface string `json:"iface"`
			Conf  string `json:"conf"`
		}](raw)
		if err != nil {
			return nil, err
		}
		if p.Iface == "" {
			return nil, errors.New("no connection to carry the tunnel")
		}
		return nil, e.vpn.Import(p.Iface, p.Conf)
	case "setPia":
		p, err := decode[struct {
			User   string  `json:"user"`
			Pass   string  `json:"pass"`
			Region *string `json:"region"`
		}](raw)
		if err != nil {
			return nil, err
		}
		if p.Region != nil {
			e.applySettings(func(s *Settings) { s.PIARegion = *p.Region })
			e.vpn.SetPIARegion(*p.Region)
		}
		if p.User != "" {
			return nil, e.vpn.SetPIA(strings.TrimSpace(p.User), p.Pass)
		}
		return nil, nil
	case "clearPia":
		return nil, e.vpn.SetPIA("", "")
	case "piaRegions":
		return e.piaRegions()
	case "setForwardedPort":
		p, err := decode[struct {
			Iface string `json:"iface"`
			Port  int    `json:"port"`
		}](raw)
		if err != nil {
			return nil, err
		}
		return nil, e.setForwardedPort(p.Iface, p.Port)
	case "removeTunnel":
		p, err := decode[struct {
			Iface string `json:"iface"`
		}](raw)
		if err != nil {
			return nil, err
		}
		return nil, e.removeTunnel(p.Iface)
	case "refreshInterfaces":
		e.refreshInterfaces()
		return nil, nil
	case "getAutostart":
		return winutil.Autostart(), nil
	case "version":
		return Version, nil
	case "setAutostart":
		p, err := decode[struct {
			On bool `json:"on"`
		}](raw)
		if err != nil {
			return nil, err
		}
		return nil, winutil.SetAutostart(p.On)
	case "quit":
		// The caller waits for this process to exit before replacing or removing it.
		e.reqOnce.Do(func() { close(e.quitReq) })
		return map[string]int{"pid": os.Getpid()}, nil
	case "openManager":
		p, _ := decode[struct {
			Hash string `json:"hash"`
		}](raw)
		e.OpenManager(p.Hash)
		return nil, nil
	}
	return nil, fmt.Errorf("unknown method %q", method)
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
	h := hello{Settings: e.settings.clone(), State: e.snapshot, Version: Version}
	e.mu.Unlock()
	h.Autostart = winutil.Autostart()

	go func() {
		for _, r := range pending {
			c.Emit("resolve", r)
		}
	}()
	return h
}
