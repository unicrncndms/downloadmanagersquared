package engine

import (
	"encoding/json"
	"errors"
	"log"
	mrand "math/rand/v2"
	"os"
	"regexp"

	"downloadmanagersquared/engine/internal/atomicfile"
	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/winutil"
)

type IfaceSettings struct {
	Name          string `json:"name,omitempty"`    // last seen name, for display while absent
	Enabled       *bool  `json:"enabled,omitempty"` // nil: default (on unless VPN)
	Connections   int    `json:"connections,omitempty"`
	Upload        *bool  `json:"upload,omitempty"`        // nil: default (on)
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
	VPNProvider        string                    `json:"vpnProvider"` // the settings page's provider choice: "", "conf" or "pia"
}

func defaultSettings() Settings {
	return Settings{
		Interfaces:         map[string]*IfaceSettings{},
		ThresholdBytes:     100_000_000, // 100 MB, shown in GB on the settings page
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
	switch s.VPNProvider {
	case "", "conf", "pia":
	default:
		s.VPNProvider = ""
	}
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

// legacyForwardedPorts are the ports typed in for imported tunnels that
// settings.json held before they were kept with the tunnels, by interface ID.
func legacyForwardedPorts(path string) map[string]int {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var s struct {
		Interfaces map[string]struct {
			ForwardedPort int `json:"forwardedPort"`
		} `json:"interfaces"`
	}
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	ports := map[string]int{}
	for id, is := range s.Interfaces {
		if is.ForwardedPort > 0 && is.ForwardedPort <= 65535 {
			ports[id] = is.ForwardedPort
		}
	}
	return ports
}

// loadSettings reads the settings. An unreadable file (any read or parse
// error) is set aside as settings.json.broken and the defaults are used.
func loadSettings(path string) (s Settings, broken bool) {
	s = defaultSettings()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		s.normalize()
		return s, false
	}
	if err == nil {
		err = json.Unmarshal(b, &s)
	}
	if err != nil {
		log.Printf("settings unreadable, set aside: %v", err)
		setAside(path)
		s = defaultSettings()
	}
	s.normalize()
	return s, err != nil
}

// setAside renames an unreadable engine file to <name>.broken, replacing an
// older one, so the user can still look at it.
func setAside(path string) {
	if err := os.Rename(path, path+".broken"); err != nil {
		log.Printf("setting aside %s: %v", path, err)
	}
}

// cookieValue is a "cookie" member's string value, also one cut off by the end
// of a damaged file.
var cookieValue = regexp.MustCompile(`("cookie"\s*:\s*)"(?:[^"\\]|\\.)*(?:"|\\?$)`)

// withoutCookies blanks every cookie value in a saved download, readable or
// encrypted, as far as the damaged text still shows one.
func withoutCookies(b []byte) []byte {
	return cookieValue.ReplaceAll(b, []byte(`${1}""`))
}

// setAsideWithoutCookies sets aside an unreadable download state like
// setAside, keeping the rest to look at but none of its cookies.
func setAsideWithoutCookies(path string, b []byte) {
	if b == nil {
		setAside(path)
		return
	}
	if err := atomicfile.Write(path+".broken", withoutCookies(b), 0o600); err != nil {
		log.Printf("setting aside %s: %v", path, err)
		return
	}
	if err := os.Remove(path); err != nil {
		log.Printf("setting aside %s: %v", path, err)
	}
}
