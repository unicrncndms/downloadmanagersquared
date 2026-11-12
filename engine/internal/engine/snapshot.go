package engine

import (
	"net"
	"sort"
	"time"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/netif"
	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/vpn"
)

type IfaceView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	IP          string   `json:"ip"` // IPv4, else IPv6
	IP6         string   `json:"ip6,omitempty"`
	Families    []string `json:"families,omitempty"` // "IPv4", "IPv6": the badge
	Present     bool     `json:"present"`
	Color       int      `json:"color"` // palette index the pages show it (and its tunnel) in
	Enabled     bool     `json:"enabled"`
	VPN         bool     `json:"vpn"`
	IsDefault   bool     `json:"isDefault"`
	IsMain      bool     `json:"isMain"`
	Connections int      `json:"connections"`
	SocksPort   int      `json:"socksPort"`
	Speed       float64  `json:"speed"`

	Upload        bool   `json:"upload"`
	UploadLimit   int64  `json:"uploadLimit"`   // bytes per second, 0 = none
	DownloadLimit int64  `json:"downloadLimit"` // bytes per second, 0 = none
	Incoming      string `json:"incoming"`      // torrent peers can connect in: "reachable", "forwarded" (a port the user typed in, unchecked), "outgoing" (only), "" (not known yet)

	// Tunnels are paths of their own, listed after the interfaces.
	Tunnel        bool   `json:"tunnel,omitempty"`
	Of            string `json:"of,omitempty"`          // a tunnel's interface
	TunnelState   string `json:"tunnelState,omitempty"` // "up", "connecting", "down" (retrying), "off" (its interface isn't used)
	TunnelError   string `json:"tunnelError,omitempty"`
	HasTunnel     bool   `json:"hasTunnel,omitempty"`     // an interface with a tunnel configured
	Provider      string `json:"provider,omitempty"`      // a tunnel's: "conf" or "pia"
	ForwardedPort int    `json:"forwardedPort,omitempty"` // an imported tunnel's port typed in by the user
	// The other enabled interfaces behind the same router (same gateway MAC),
	// by name: together they are no faster than one. Only a warning.
	SameRouter []string `json:"sameRouter,omitempty"`
}

type StateEvent struct {
	Downloads  []download.Summary      `json:"downloads"`
	Groups     []download.GroupSummary `json:"groups"` // each Group of two or more downloads
	Interfaces []IfaceView             `json:"interfaces"`
	PIA        vpn.PIAView             `json:"pia"`
	Speed      float64                 `json:"speed"`
	ETA        float64                 `json:"eta"`
	Active     int                     `json:"active"`
	Update     *UpdateView             `json:"update,omitempty"`   // a newer release exists
	IPv6Only   []string                `json:"ipv6Only,omitempty"` // adapters left out because they have only IPv6 and IPv6 is off
}

func (e *Engine) updateSnapshot(dt time.Duration) {
	sums := []download.Summary{}
	browsers := []string{} // each download's browser, for Record recipe
	for _, d := range e.queue.List() {
		sums = append(sums, d.Summary())
		b := ""
		if hd, ok := d.(*download.Download); ok {
			b = hd.Browser()
		}
		browsers = append(browsers, b)
	}
	speeds := map[string]float64{}
	var total float64
	var remaining int64
	active := 0
	for _, s := range sums {
		for id, l := range s.Links {
			speeds[id] += l.Speed
		}
		if s.Status == download.StatusRunning {
			total += s.Speed
			active++
		}
		// Time left is for all that will download, not only what runs now:
		// a split archive's parts queued behind the one running count too.
		if (s.Status == download.StatusRunning || s.Status == download.StatusQueued) && s.Size > 0 {
			remaining += s.Size - s.Done
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range sums {
		sums[i].Actions.RecordRecipe = e.canRecordLocked(sums[i], browsers[i])
	}
	now := time.Now()
	e.cum += total * dt.Seconds()
	e.meter.Window = time.Duration(e.settings.ETAWindowSec) * time.Second
	e.meter.Add(now, int64(e.cum))
	if active == 0 {
		e.meter.Reset()
		e.eta.Clear()
	} else {
		e.eta.Update(now, remaining, e.meter.Rate())
	}

	mainID := paths.MainInterface(e.pathSnapshotLocked())
	tunnels := e.vpn.Status(now)
	hasTunnel := map[string]bool{}
	for _, st := range tunnels {
		hasTunnel[st.Iface] = true
	}
	var used []netif.Interface
	for _, ent := range e.ifaces {
		if ent.present && e.enabledLocked(ent) {
			used = append(used, ent.info)
		}
	}
	sameRouter := netif.SameRouter(used)
	views := []IfaceView{}
	for id, ent := range e.ifaces {
		var same []string
		for _, other := range sameRouter[id] {
			same = append(same, e.ifaces[other].info.Name)
		}
		sort.Strings(same)
		up, down := ent.shaper.Limits()
		views = append(views, IfaceView{
			ID: id, Name: ent.info.Name, Description: ent.info.Description, IP: ipText(ent.info.IP()), IP6: ipText(ent.info.IPv6),
			Families: ent.info.Families(),
			Present:  ent.present, Color: e.colorLocked(id), Enabled: e.enabledLocked(ent), VPN: ent.info.VPN,
			IsDefault: ent.info.IsDefault, IsMain: id == mainID, Connections: e.connsLocked(id),
			SocksPort: ent.socks.Port(), Speed: speeds[id],
			Upload: e.uploadLocked(id), UploadLimit: up, DownloadLimit: down,
			Incoming: incomingText(ent.incoming), HasTunnel: hasTunnel[id], SameRouter: same,
		})
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Present != views[j].Present {
			return views[i].Present
		}
		return views[i].Name < views[j].Name
	})
	tviews := e.tunnelViewsLocked(tunnels, speeds)
	sort.Slice(tviews, func(i, j int) bool { return tviews[i].Name < tviews[j].Name })
	views = append(views, tviews...)
	e.snapshot = StateEvent{Downloads: sums, Groups: download.Groups(sums), Interfaces: views, PIA: e.vpn.PIA(), Speed: total, ETA: e.eta.Seconds(), Active: active, Update: e.updateViewLocked(), IPv6Only: e.ipv6Only}
}

func ipText(ip net.IP) string {
	if ip == nil {
		return ""
	}
	return ip.String()
}

func incomingText(in incoming) string {
	switch {
	case in.checked.IsZero():
		return ""
	case in.reachable:
		return "reachable"
	}
	return "outgoing"
}

func (e *Engine) Snapshot() StateEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snapshot
}
