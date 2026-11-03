package engine

import (
	"encoding/json"
	mrand "math/rand/v2"
	"os"
	"path/filepath"

	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/winutil"
)

type IfaceSettings struct {
	Name          string `json:"name,omitempty"`    // last seen name, for display while absent
	Enabled       *bool  `json:"enabled,omitempty"` // nil: default (on unless metered or VPN)
	Connections   int    `json:"connections,omitempty"`
	Upload        *bool  `json:"upload,omitempty"`        // nil: default (on unless metered)
	UploadLimit   int64  `json:"uploadLimit,omitempty"`   // bytes per second, 0 = none
	DownloadLimit int64  `json:"downloadLimit,omitempty"` // bytes per second, 0 = none
}

type Settings struct {
	Interfaces         map[string]*IfaceSettings `json:"interfaces"`
	MainInterface      string                    `json:"mainInterface"` // "" = Windows default route
	ThresholdBytes     int64                     `json:"thresholdBytes"`
	SaveDir            string                    `json:"saveDir"`
	MaxParallel        int                       `json:"maxParallel"`
	ETAWindowSec       int                       `json:"etaWindowSec"`
	DefaultConnections int                       `json:"defaultConnections"`
	SeedRatio          float64                   `json:"seedRatio"`   // seed a torrent until uploaded/size reaches this; 0 = don't seed
	TorrentPort        int                       `json:"torrentPort"` // where peers connect in, on every interface; picked once
	VPNHTTP            paths.Mode                `json:"vpnHttp"`     // VPN mode an HTTP download gets unless chosen when added
	VPNTorrent         paths.Mode                `json:"vpnTorrent"`  // the same for a torrent
	PIARegion          string                    `json:"piaRegion"`   // "" = the lowest latency
}

func defaultSettings() Settings {
	return Settings{
		Interfaces:         map[string]*IfaceSettings{},
		ThresholdBytes:     30_000_000_000, // 30 GB, as shown in the settings page
		SaveDir:            winutil.DownloadsFolder(),
		MaxParallel:        1,
		ETAWindowSec:       60,
		DefaultConnections: 4,
		SeedRatio:          1,
		VPNHTTP:            paths.Off,
		VPNTorrent:         paths.Off, // each new torrent asks first
	}
}

func (s *Settings) normalize() {
	d := defaultSettings()
	if s.Interfaces == nil {
		s.Interfaces = map[string]*IfaceSettings{}
	}
	if s.ThresholdBytes <= 0 {
		s.ThresholdBytes = d.ThresholdBytes
	}
	if s.SaveDir == "" {
		s.SaveDir = d.SaveDir
	}
	s.MaxParallel = min(max(s.MaxParallel, 1), 16)
	if s.ETAWindowSec < 5 {
		s.ETAWindowSec = d.ETAWindowSec
	}
	s.ETAWindowSec = min(s.ETAWindowSec, 3600)
	if s.DefaultConnections < 1 {
		s.DefaultConnections = d.DefaultConnections
	}
	s.DefaultConnections = min(s.DefaultConnections, 32)
	s.SeedRatio = min(max(s.SeedRatio, 0), 100)
	s.VPNHTTP = paths.ParseMode(string(s.VPNHTTP), d.VPNHTTP)
	s.VPNTorrent = paths.ParseMode(string(s.VPNTorrent), d.VPNTorrent)
	if s.TorrentPort < 1024 || s.TorrentPort > 65535 {
		s.TorrentPort = 20000 + mrand.IntN(40000)
	}
	for _, is := range s.Interfaces {
		if is.Connections != 0 { // 0 = use DefaultConnections
			is.Connections = min(max(is.Connections, 1), 32)
		}
		is.UploadLimit, is.DownloadLimit = max(is.UploadLimit, 0), max(is.DownloadLimit, 0)
	}
}

func loadSettings(path string) Settings {
	s := defaultSettings()
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &s)
	}
	s.normalize()
	return s
}

// writeJSON saves atomically, so a crash never leaves a half-written file.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
