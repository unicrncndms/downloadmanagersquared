package engine

import (
	"errors"
	"strings"
	"time"

	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/queue"
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
		if p.PeersPerLine != nil {
			s.PeersPerLine = *p.PeersPerLine
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
		if p.PartsParallel != nil {
			s.PartsParallel = *p.PartsParallel
		}
		if p.ArchiveKeeping != nil {
			s.ArchiveKeeping = *p.ArchiveKeeping // normalize puts back one that isn't a choice
		}
		if p.IPv6 != nil {
			s.IPv6 = *p.IPv6
		}
		if p.NotifyDone != nil {
			s.NotifyDone = *p.NotifyDone
		}
		if p.KeepAwake != nil {
			s.KeepAwake = *p.KeepAwake
		}
		if p.SpeedUnit != nil {
			s.SpeedUnit = *p.SpeedUnit // normalize puts back one that isn't a choice
		}
		if p.TorrentPort != nil {
			s.TorrentPort = *p.TorrentPort // normalize picks another when out of range
		}
		if p.NeverCapture != nil {
			s.NeverCapture = *p.NeverCapture
		}
		if p.OnExists != nil {
			s.OnExists = *p.OnExists // normalize puts back one that isn't a choice
		}
		if p.FolderRules != nil {
			s.FolderRules = *p.FolderRules // normalize drops the ones that are no rule
		}
		if p.AlwaysCapture != nil {
			s.AlwaysCapture = *p.AlwaysCapture
		}
		if p.Theme != nil {
			s.Theme = *p.Theme
		}
		if p.QueueWindow != nil {
			s.QueueWindow = *p.QueueWindow // normalize clears a day that is no window
		}
		if p.Accent != nil {
			s.Accent = *p.Accent
		}
		if p.ThemeAccent != nil {
			s.ThemeAccent = *p.ThemeAccent
		}
		if p.Tint != nil {
			s.Tint = *p.Tint
		}
		if p.DismissedUpdate != nil {
			s.DismissedUpdate = *p.DismissedUpdate
		}
		if p.SeenVersion != nil {
			s.SeenVersion = *p.SeenVersion
		}
		if p.FoldedSections != nil {
			s.FoldedSections = *p.FoldedSections
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

// restoreSettings puts the settings page's choices back to the defaults.
func (e *Engine) restoreSettings() Settings {
	return e.applySettings(func(s *Settings) { *s = s.restored() })
}

// KeepAwake is the keepAwake setting, for the tray.
func (e *Engine) KeepAwake() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.settings.KeepAwake
}

// SpeedUnit is the speedUnit setting, for the tray.
func (e *Engine) SpeedUnit() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.settings.SpeedUnit
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
		if p.Label != nil {
			is.Label = cleanLabel(*p.Label)
		}
		if p.PIARegion != nil {
			is.PIARegion = *p.PIARegion
		}
		if p.Color != nil && *p.Color >= 0 && *p.Color < colorCount {
			v := *p.Color
			is.Color = &v
		}
		if p.TunnelColor != nil && *p.TunnelColor >= 0 && *p.TunnelColor < colorCount {
			v := *p.TunnelColor
			is.TunnelColor = &v
		}
		if p.Hidden != nil {
			is.Hidden = *p.Hidden
		}
	})
}

type settingsPatch struct {
	MainInterface      *string       `json:"mainInterface"`
	ThresholdBytes     *int64        `json:"thresholdBytes"`
	SaveDir            *string       `json:"saveDir"`
	MaxParallel        *int          `json:"maxParallel"`
	UploadLimit        *int64        `json:"uploadLimit"`
	DownloadLimit      *int64        `json:"downloadLimit"`
	ETAWindowSec       *int          `json:"etaWindowSec"`
	DefaultConnections *int          `json:"defaultConnections"`
	SeedRatio          *float64      `json:"seedRatio"`
	PeersPerLine       *int          `json:"peersPerLine"`
	VPNHTTP            *string       `json:"vpnHttp"`
	VPNTorrent         *string       `json:"vpnTorrent"`
	VPNProvider        *string       `json:"vpnProvider"`
	FolderDefault      *bool         `json:"folderDefault"`
	PartsParallel      *bool         `json:"partsParallel"`
	ArchiveKeeping     *string       `json:"archiveKeeping"`
	IPv6               *bool         `json:"ipv6"`
	NotifyDone         *bool         `json:"notifyDone"`
	KeepAwake          *bool         `json:"keepAwake"`
	SpeedUnit          *string       `json:"speedUnit"`
	TorrentPort        *int          `json:"torrentPort"`
	NeverCapture       *[]string     `json:"neverCapture"`
	OnExists           *string       `json:"onExists"`
	FolderRules        *[]FolderRule `json:"folderRules"`
	AlwaysCapture      *[]string     `json:"alwaysCapture"`
	Theme              *string       `json:"theme"`
	QueueWindow        *queue.Week   `json:"queueWindow"`
	Accent             *string       `json:"accent"`
	ThemeAccent        *string       `json:"themeAccent"`
	Tint               *int          `json:"tint"`
	DismissedUpdate    *string       `json:"dismissedUpdate"`
	SeenVersion        *string       `json:"seenVersion"`
	FoldedSections     *[]string     `json:"foldedSections"`
}

type ifacePatch struct {
	ID            string  `json:"id"`
	Enabled       *bool   `json:"enabled"`
	Connections   *int    `json:"connections"`
	Upload        *bool   `json:"upload"`
	UploadLimit   *int64  `json:"uploadLimit"`
	DownloadLimit *int64  `json:"downloadLimit"`
	Color         *int    `json:"color"`       // 0..5; anything else is ignored
	Label         *string `json:"label"`       // "" gives the Windows name back
	PIARegion     *string `json:"piaRegion"`   // "" follows PIA's own region, "auto" takes one no other tunnel uses
	TunnelColor   *int    `json:"tunnelColor"` // its tunnel's colour, 0..5
	Hidden        *bool   `json:"hidden"`      // folded away and never used
}

// cleanLabel is a name the user typed: trimmed, at most 40 characters.
func cleanLabel(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > 40 {
		r = r[:40]
	}
	return string(r)
}

// forgetInterface drops what is saved about an interface that is not connected:
// its settings, its tunnel and its place in the list.
func (e *Engine) forgetInterface(id string) error {
	var err error
	e.applySettings(func(s *Settings) {
		if ent := e.ifaces[id]; ent != nil {
			if ent.present {
				err = errors.New("that connection is in use now")
				return
			}
			ent.socks.Close()
			if ent.binding != nil {
				ent.binding.Close()
			}
			delete(e.ifaces, id)
		}
		delete(s.Interfaces, id)
		if s.MainInterface == id {
			s.MainInterface = ""
		}
	})
	if err == nil {
		_ = e.vpn.Remove(id) // none configured is fine
	}
	return err
}

func (e *Engine) applySettings(f func(*Settings)) Settings {
	e.mu.Lock()
	f(&e.settings)
	e.settings.normalize()
	e.keepingNow()
	e.saveSettingsLocked()
	e.applyLimitsLocked()
	s := e.settings.clone()
	e.mu.Unlock()
	e.tickVPN(time.Now()) // a tunnel follows its interface being switched off or on
	e.syncTorrentIfaces() // enabled interfaces, upload or the main one may have changed
	e.broadcast("settings", s)
	return s
}
