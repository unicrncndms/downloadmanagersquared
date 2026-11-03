// Package paths decides which paths a download may use under its VPN mode: bare
// interfaces, their tunnels, or both, and which of them is the main path.
package paths

import (
	"slices"
	"strings"
)

// Mode is a download's VPN mode, fixed when it is added.
type Mode string

const (
	Off       Mode = "off"       // bare interfaces only
	Alongside Mode = "alongside" // bare interfaces and their tunnels
	VPNOnly   Mode = "vpn"       // tunnels only; wait while none is up
)

// Iface is what the policy needs to know about one interface.
type Iface struct {
	ID       string
	Present  bool
	Enabled  bool
	Default  bool // carries the Windows default route
	TunnelUp bool
}

// Snapshot is the interfaces at one moment, and the main interface the user chose.
type Snapshot struct {
	Main   string
	Ifaces []Iface
}

// Result is the paths a mode allows, the main path first.
type Result struct {
	Paths []string
	Wait  bool // VPN only and no tunnel is up: the download must not run
}

// Main is the main path, "" when there is none.
func (r Result) Main() string {
	if len(r.Paths) == 0 {
		return ""
	}
	return r.Paths[0]
}

// For decides the paths a download in mode m may use. The main path is the
// main interface, or under VPN only its tunnel, else the first tunnel up.
func For(m Mode, s Snapshot) Result {
	usable := s.usable()
	main := MainInterface(s)
	var r Result
	for _, i := range usable {
		if m != VPNOnly {
			r.Paths = append(r.Paths, i.ID)
		}
		if m != Off && i.TunnelUp {
			r.Paths = append(r.Paths, Tunnel(i.ID))
		}
	}
	if m == VPNOnly {
		main = Tunnel(main)
	}
	slices.SortFunc(r.Paths, func(a, b string) int {
		switch {
		case a == main:
			return -1
		case b == main:
			return 1
		}
		return strings.Compare(a, b)
	})
	r.Wait = m == VPNOnly && len(r.Paths) == 0
	return r
}

// MainInterface is the chosen main interface, else the one with the Windows
// default route, else the lowest ID; "" when none is present and enabled.
func MainInterface(s Snapshot) string {
	return mainOf(s.Main, s.usable())
}

// ParseMode reads a mode name, "" or unknown giving def.
func ParseMode(s string, def Mode) Mode {
	switch m := Mode(s); m {
	case Off, Alongside, VPNOnly:
		return m
	}
	return def
}

// A tunnel is a path of its own: downloads, links and the resolver know it by
// its interface's ID with this suffix.
const tunnelSuffix = "~vpn"

// Tunnel is the path ID of an interface's tunnel.
func Tunnel(ifaceID string) string { return ifaceID + tunnelSuffix }

// IfaceOf splits a path ID into its interface and whether it is the tunnel.
func IfaceOf(id string) (string, bool) {
	if i, ok := strings.CutSuffix(id, tunnelSuffix); ok {
		return i, true
	}
	return id, false
}

// usable are the interfaces present and enabled.
func (s Snapshot) usable() []Iface {
	var out []Iface
	for _, i := range s.Ifaces {
		if i.Present && i.Enabled {
			out = append(out, i)
		}
	}
	return out
}

// mainOf: the chosen main interface, else the Windows default route, else the lowest ID.
func mainOf(chosen string, usable []Iface) string {
	var fallback string
	for _, i := range usable {
		if i.ID == chosen {
			return chosen
		}
	}
	for _, i := range usable {
		if i.Default {
			return i.ID
		}
		if fallback == "" || i.ID < fallback {
			fallback = i.ID
		}
	}
	return fallback
}
