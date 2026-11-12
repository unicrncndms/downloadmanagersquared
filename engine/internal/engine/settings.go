package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	mrand "math/rand/v2"
	"net"
	"os"
	"regexp"
	"slices"
	"strings"

	"downloadmanagersquared/engine/internal/atomicfile"
	"downloadmanagersquared/engine/internal/download"
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
	Color         *int   `json:"color,omitempty"`         // palette index 0..colorCount-1 the pages show it in; nil: not given yet
}

// colorCount is how many colours the pages' palette has (ui.css --c0 …).
const colorCount = 6

// assignColors gives each of ids (in the order they are to be served) that has
// no colour yet the lowest palette index no other known interface uses, and says
// whether it gave any. Colours are kept so one never changes when another
// interface turns up; with every colour taken, the least used one is shared.
func (s *Settings) assignColors(ids []string) bool {
	var used [colorCount]int
	for _, is := range s.Interfaces {
		if is.Color != nil {
			used[*is.Color]++
		}
	}
	changed := false
	for _, id := range ids {
		is := s.Interfaces[id]
		if is == nil || is.Color != nil {
			continue
		}
		c := 0
		for i, n := range used {
			if n < used[c] {
				c = i
			}
		}
		used[c]++
		is.Color, changed = &c, true
	}
	return changed
}

type Settings struct {
	Interfaces         map[string]*IfaceSettings `json:"interfaces"`
	MainInterface      string                    `json:"mainInterface"` // "" = Windows default route
	ThresholdBytes     int64                     `json:"thresholdBytes"`
	SaveDir            string                    `json:"saveDir"`
	MaxParallel        int                       `json:"maxParallel"`
	UploadLimit        int64                     `json:"uploadLimit,omitempty"`   // across every interface, bytes per second, 0 = none
	DownloadLimit      int64                     `json:"downloadLimit,omitempty"` // the same
	ETAWindowSec       int                       `json:"etaWindowSec"`
	DefaultConnections int                       `json:"defaultConnections"`
	SeedRatio          float64                   `json:"seedRatio"`   // seed a torrent until uploaded/size reaches this; 0 = don't seed
	TorrentPort        int                       `json:"torrentPort"` // where peers connect in, on every interface; picked once
	VPNHTTP            paths.Mode                `json:"vpnHttp"`     // VPN mode an HTTP download gets unless chosen when added
	VPNTorrent         paths.Mode                `json:"vpnTorrent"`  // the same for a torrent
	PIARegion          string                    `json:"piaRegion"`   // "" = the lowest latency
	VPNProvider        string                    `json:"vpnProvider"` // the settings page's provider choice: "", "conf" or "pia"
	IPv6               bool                      `json:"ipv6"`        // Enable IPv6 (experimental); off: IPv4 only, no AAAA lookups, no IPv6 sockets

	// Download as folder
	FolderDefault  bool     `json:"folderDefault"`        // new .zip, .7z and .rar downloads are one
	PartsParallel  bool     `json:"partsParallel"`        // a split archive's parts download at once even where that keeps more on disk
	ArchiveKeeping string   `json:"archiveKeeping"`       // what one does with its archive unless chosen for it: download.KeepAuto, KeepFree, KeepUntil or KeepAfter (ADR 0007)
	FolderDir      string   `json:"folderDir,omitempty"`  // the folder last extracted into; "" = SaveDir
	FolderDirs     []string `json:"folderDirs,omitempty"` // the folders last extracted into, newest first: Change… offers them

	// The pages' accent color, "#rrggbb": the one chosen, else Firefox's
	// theme's as the extension last saw it, else the pages' own.
	Accent      string `json:"accent,omitempty"`      // "" = follow Firefox
	ThemeAccent string `json:"themeAccent,omitempty"` // "" = Firefox's theme has none
}

// clone is a copy that shares nothing with s, to read (and encode) after the
// engine's lock is released: encoding the engine's own map while an interface
// is added to it would stop the process.
func (s Settings) clone() Settings {
	ifs := make(map[string]*IfaceSettings, len(s.Interfaces))
	for id, is := range s.Interfaces {
		c := *is
		ifs[id] = &c
	}
	s.Interfaces = ifs
	s.FolderDirs = slices.Clone(s.FolderDirs)
	return s
}

var hexColor = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// pickTorrentPort draws ports from pick until one can be bound: Windows
// reserves ranges (Hyper-V, dynamic port exclusions) where a listen is
// refused, which would leave torrents with outgoing peers only for good. After
// 50 refusals it keeps the last, as before.
func pickTorrentPort(pick func() int) int {
	p := pick()
	for i := 0; i < 50 && !bindable(p); i++ {
		p = pick()
	}
	return p
}

// bindable: TCP and UDP can both listen on port p on every address now.
func bindable(p int) bool {
	addr := fmt.Sprintf("0.0.0.0:%d", p)
	l, err := net.Listen("tcp4", addr)
	if err != nil {
		return false
	}
	l.Close()
	u, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return false
	}
	u.Close()
	return true
}

// maxRecentFolders is how many folders extracted into are remembered.
const maxRecentFolders = 5

// useFolder makes dir the last folder extracted into, at the front of the
// recent ones.
func (s *Settings) useFolder(dir string) {
	s.FolderDir = dir
	recent := []string{dir}
	for _, d := range s.FolderDirs {
		if !strings.EqualFold(d, dir) && len(recent) < maxRecentFolders {
			recent = append(recent, d)
		}
	}
	s.FolderDirs = recent
}

// folderDir is where a Download as folder extracts unless told otherwise:
// the last folder used for one, else the Save folder.
func (s *Settings) folderDir() string {
	if s.FolderDir != "" {
		return s.FolderDir
	}
	return s.SaveDir
}

func defaultSettings() Settings {
	return Settings{
		Interfaces:         map[string]*IfaceSettings{},
		ThresholdBytes:     100_000_000, // 100 MB, shown in GB on the settings page
		SaveDir:            winutil.DownloadsFolder(),
		MaxParallel:        1,
		ETAWindowSec:       60,
		DefaultConnections: 4,
		PartsParallel:      true,
		ArchiveKeeping:     download.KeepAuto,
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
	s.UploadLimit, s.DownloadLimit = max(s.UploadLimit, 0), max(s.DownloadLimit, 0)
	if s.ETAWindowSec < 5 {
		s.ETAWindowSec = d.ETAWindowSec
	}
	s.ETAWindowSec = min(s.ETAWindowSec, 3600)
	if s.DefaultConnections < 1 {
		s.DefaultConnections = d.DefaultConnections
	}
	s.DefaultConnections = min(s.DefaultConnections, 32)
	s.SeedRatio = min(max(s.SeedRatio, 0), 100)
	if !download.KeepChoice(s.ArchiveKeeping) {
		s.ArchiveKeeping = d.ArchiveKeeping
	}
	s.VPNHTTP = paths.ParseMode(string(s.VPNHTTP), d.VPNHTTP)
	s.VPNTorrent = paths.ParseMode(string(s.VPNTorrent), d.VPNTorrent)
	switch s.VPNProvider {
	case "", "conf", "pia":
	default:
		s.VPNProvider = ""
	}
	for _, c := range []*string{&s.Accent, &s.ThemeAccent} {
		if *c = strings.ToLower(*c); !hexColor.MatchString(*c) {
			*c = ""
		}
	}
	if s.TorrentPort < 1024 || s.TorrentPort > 65535 {
		s.TorrentPort = pickTorrentPort(func() int { return 20000 + mrand.IntN(40000) })
	}
	for _, is := range s.Interfaces {
		if is.Connections != 0 { // 0 = use DefaultConnections
			is.Connections = min(max(is.Connections, 1), 32)
		}
		if is.Color != nil && (*is.Color < 0 || *is.Color >= colorCount) {
			is.Color = nil // assigned again when the interface is next seen
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
