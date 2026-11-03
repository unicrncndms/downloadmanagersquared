package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"downloadmanagersquared/engine/internal/ipc"
	"downloadmanagersquared/engine/internal/paths"
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

type resolvedParams struct {
	ID      string `json:"id"`
	Iface   string `json:"iface"`
	URL     string `json:"url"`
	Cookie  string `json:"cookie"`
	Referer string `json:"referer"`
	Error   string `json:"error"`
}

type settingsPatch struct {
	MainInterface      *string  `json:"mainInterface"`
	ThresholdBytes     *int64   `json:"thresholdBytes"`
	SaveDir            *string  `json:"saveDir"`
	MaxParallel        *int     `json:"maxParallel"`
	ETAWindowSec       *int     `json:"etaWindowSec"`
	DefaultConnections *int     `json:"defaultConnections"`
	SeedRatio          *float64 `json:"seedRatio"`
	VPNHTTP            *string  `json:"vpnHttp"`
	VPNTorrent         *string  `json:"vpnTorrent"`
}

type ifacePatch struct {
	ID            string `json:"id"`
	Enabled       *bool  `json:"enabled"`
	Connections   *int   `json:"connections"`
	Upload        *bool  `json:"upload"`
	UploadLimit   *int64 `json:"uploadLimit"`
	DownloadLimit *int64 `json:"downloadLimit"`
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
		d.SetHashes(p.SHA256, p.MD5)
		return nil, nil
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
		return e.settings, nil
	case "setSettings":
		p, err := decode[settingsPatch](raw)
		if err != nil {
			return nil, err
		}
		return e.applySettings(func(s *Settings) {
			if p.MainInterface != nil {
				s.MainInterface = *p.MainInterface
			}
			if p.ThresholdBytes != nil {
				s.ThresholdBytes = *p.ThresholdBytes
			}
			if p.SaveDir != nil {
				s.SaveDir = *p.SaveDir
			}
			if p.MaxParallel != nil {
				s.MaxParallel = *p.MaxParallel
			}
			if p.ETAWindowSec != nil {
				s.ETAWindowSec = *p.ETAWindowSec
			}
			if p.DefaultConnections != nil {
				s.DefaultConnections = *p.DefaultConnections
			}
			if p.SeedRatio != nil {
				s.SeedRatio = *p.SeedRatio
			}
			if p.VPNHTTP != nil {
				s.VPNHTTP = paths.ParseMode(*p.VPNHTTP, s.VPNHTTP)
			}
			if p.VPNTorrent != nil {
				s.VPNTorrent = paths.ParseMode(*p.VPNTorrent, s.VPNTorrent)
			}
		}), nil
	case "setInterface":
		p, err := decode[ifacePatch](raw)
		if err != nil {
			return nil, err
		}
		return e.applySettings(func(s *Settings) {
			is := s.Interfaces[p.ID]
			if is == nil {
				is = &IfaceSettings{}
				s.Interfaces[p.ID] = is
			}
			if p.Enabled != nil {
				v := *p.Enabled
				is.Enabled = &v
			}
			if p.Connections != nil {
				is.Connections = *p.Connections
			}
			if p.Upload != nil {
				v := *p.Upload
				is.Upload = &v
			}
			if p.UploadLimit != nil {
				is.UploadLimit = *p.UploadLimit
			}
			if p.DownloadLimit != nil {
				is.DownloadLimit = *p.DownloadLimit
			}
		}), nil
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
		if err := e.setTunnel(p.Iface, p.Conf, true); err != nil {
			return nil, fmt.Errorf("not a usable WireGuard config: %w", err)
		}
		e.syncTunnels(time.Now())
		return nil, nil
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
			e.restartPIATunnels()
		}
		if p.User != "" {
			return nil, e.setPIA(strings.TrimSpace(p.User), p.Pass)
		}
		return nil, nil
	case "clearPia":
		return nil, e.setPIA("", "")
	case "piaRegions":
		return e.piaRegions()
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

func (e *Engine) applySettings(f func(*Settings)) Settings {
	e.mu.Lock()
	f(&e.settings)
	e.settings.normalize()
	e.saveSettingsLocked()
	e.applyLimitsLocked()
	s := e.settings
	e.mu.Unlock()
	e.syncTorrentIfaces() // enabled interfaces, upload or the main one may have changed
	e.broadcast("settings", s)
	return s
}

// hello registers the caller as the browser extension and hands it anything it
// missed. The browser's user agent is kept for downloads added without one.
// The browser's exe is kept to open pages in that browser.
func (e *Engine) hello(c *ipc.Conn, p helloParams) hello {
	e.mu.Lock()
	if cl := e.clients[c]; cl != nil {
		cl.extension = true
	}
	if p.UserAgent != "" {
		e.userAgent = p.UserAgent
	}
	if p.Browser != "" {
		e.browser = p.Browser
	}
	pending := make([]resolveReq, 0, len(e.resolves))
	for _, r := range e.resolves {
		pending = append(pending, r)
	}
	open := e.openWant
	e.openWant = nil
	h := hello{Settings: e.settings, State: e.snapshot, Version: Version}
	e.mu.Unlock()
	h.Autostart = winutil.Autostart()

	go func() {
		for _, r := range pending {
			c.Emit("resolve", r)
		}
		if open != nil {
			c.Emit("openManager", map[string]string{"hash": *open})
		}
	}()
	return h
}
