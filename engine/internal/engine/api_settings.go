package engine

import (
	"time"

	"downloadmanagersquared/engine/internal/paths"
)

// setSettings changes the settings the patch names and applies what follows
// from them.
func (e *Engine) setSettings(p settingsPatch) Settings {
	s := e.applySettings(func(s *Settings) {
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
		if p.UploadLimit != nil {
			s.UploadLimit = *p.UploadLimit
		}
		if p.DownloadLimit != nil {
			s.DownloadLimit = *p.DownloadLimit
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
		if p.VPNProvider != nil {
			s.VPNProvider = *p.VPNProvider
		}
		if p.FolderDefault != nil {
			s.FolderDefault = *p.FolderDefault
		}
		if p.IPv6 != nil {
			s.IPv6 = *p.IPv6
		}
		if p.Accent != nil {
			s.Accent = *p.Accent
		}
		if p.ThemeAccent != nil {
			s.ThemeAccent = *p.ThemeAccent
		}
	})
	if p.IPv6 != nil {
		e.refreshInterfaces() // IPv6-only interfaces come or go, the others get new bindings
	}
	// The VPN modes apply to every download of that type, not only new ones.
	if p.VPNHTTP != nil {
		e.setVPNAll(false, s.VPNHTTP)
	}
	if p.VPNTorrent != nil {
		e.setVPNAll(true, s.VPNTorrent)
	}
	return s
}

// setInterface changes the settings of one interface.
func (e *Engine) setInterface(p ifacePatch) Settings {
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
	})
}

type settingsPatch struct {
	MainInterface      *string  `json:"mainInterface"`
	ThresholdBytes     *int64   `json:"thresholdBytes"`
	SaveDir            *string  `json:"saveDir"`
	MaxParallel        *int     `json:"maxParallel"`
	UploadLimit        *int64   `json:"uploadLimit"`
	DownloadLimit      *int64   `json:"downloadLimit"`
	ETAWindowSec       *int     `json:"etaWindowSec"`
	DefaultConnections *int     `json:"defaultConnections"`
	SeedRatio          *float64 `json:"seedRatio"`
	VPNHTTP            *string  `json:"vpnHttp"`
	VPNTorrent         *string  `json:"vpnTorrent"`
	VPNProvider        *string  `json:"vpnProvider"`
	FolderDefault      *bool    `json:"folderDefault"`
	IPv6               *bool    `json:"ipv6"`
	Accent             *string  `json:"accent"`
	ThemeAccent        *string  `json:"themeAccent"`
}

type ifacePatch struct {
	ID            string `json:"id"`
	Enabled       *bool  `json:"enabled"`
	Connections   *int   `json:"connections"`
	Upload        *bool  `json:"upload"`
	UploadLimit   *int64 `json:"uploadLimit"`
	DownloadLimit *int64 `json:"downloadLimit"`
}

func (e *Engine) applySettings(f func(*Settings)) Settings {
	e.mu.Lock()
	f(&e.settings)
	e.settings.normalize()
	e.saveSettingsLocked()
	e.applyLimitsLocked()
	s := e.settings.clone()
	e.mu.Unlock()
	e.tickVPN(time.Now()) // a tunnel follows its interface being switched off or on
	e.syncTorrentIfaces() // enabled interfaces, upload or the main one may have changed
	e.broadcast("settings", s)
	return s
}
