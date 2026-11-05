package engine

import (
	"errors"
	"time"

	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/vpn"
)

// vpnOptions lets tests point the tunnels at fake servers.
var vpnOptions = func(*vpn.Options) {}

// loadVPN reads the saved tunnel configs and PIA login, telling the user about
// any that had to be set aside.
func (e *Engine) loadVPN() {
	o := vpn.Options{Dir: e.dir, PIARegion: e.settings.PIARegion}
	vpnOptions(&o)
	e.vpn = vpn.New(o)
	for _, p := range e.vpn.Load() {
		if p.Iface == "" {
			notify("PIA login unreadable", "PIA login couldn't be read, sign in again.", nil)
			continue
		}
		name := p.Iface
		if s := e.settings.Interfaces[p.Iface]; s != nil && s.Name != "" {
			name = s.Name
		}
		notify("VPN config unreadable", "VPN config for "+name+" couldn't be read, import it again.", nil)
	}
}

// tickVPN hands the tunnels the interfaces as they are now.
func (e *Engine) tickVPN(now time.Time) {
	e.mu.Lock()
	ifs := e.vpnIfacesLocked()
	e.mu.Unlock()
	e.vpn.Tick(now, ifs)
}

// vpnIfacesLocked are the interfaces as the tunnels see them, with those only
// the settings remember (for their names).
func (e *Engine) vpnIfacesLocked() []vpn.Iface {
	var out []vpn.Iface
	for id, ent := range e.ifaces {
		i := vpn.Iface{ID: id, Name: ent.info.Name, Binding: ent.binding, Usable: ent.present && e.enabledLocked(ent)}
		if s := e.settings.Interfaces[id]; s != nil {
			i.ForwardedPort = s.ForwardedPort
		}
		out = append(out, i)
	}
	for id, s := range e.settings.Interfaces {
		if e.ifaces[id] == nil {
			out = append(out, vpn.Iface{ID: id, Name: s.Name, ForwardedPort: s.ForwardedPort})
		}
	}
	return out
}

// tunnelUpLocked is whether an interface's tunnel is up.
func (e *Engine) tunnelUpLocked(ifaceID string) bool {
	_, ok := e.vpn.Up(ifaceID)
	return ok
}

// tunnelStatus is the status of an interface's tunnel, if it has one.
func (e *Engine) tunnelStatus(ifaceID string) (vpn.Status, bool) {
	for _, st := range e.vpn.Status(time.Now()) {
		if st.Iface == ifaceID {
			return st, true
		}
	}
	return vpn.Status{}, false
}

func (e *Engine) tunnelViewsLocked(tunnels []vpn.Status, speeds map[string]float64) []IfaceView {
	var out []IfaceView
	for _, st := range tunnels {
		id := st.Iface
		ent := e.ifaces[id]
		name := id
		if ent != nil {
			name = ent.info.Name
		} else if s := e.settings.Interfaces[id]; s != nil && s.Name != "" {
			name = s.Name
		}
		v := IfaceView{
			ID: paths.Tunnel(id), Name: "VPN via " + name, Tunnel: true, Of: id, IP: st.Address,
			Present: st.State == "up", Enabled: true, Connections: e.connsLocked(id),
			SocksPort: st.SocksPort, Speed: speeds[paths.Tunnel(id)], Incoming: st.Incoming,
			TunnelState: st.State, TunnelError: st.Err, Provider: st.Provider, ForwardedPort: st.ForwardedPort,
		}
		if ent != nil {
			// A tunnel follows its interface's upload switch and shares its limits.
			v.Upload = e.uploadLocked(id)
			v.UploadLimit, v.DownloadLimit = ent.shaper.Limits()
		}
		out = append(out, v)
	}
	return out
}

// piaRegions lists PIA's regions for the settings page, through the main interface.
func (e *Engine) piaRegions() ([]map[string]any, error) {
	e.mu.Lock()
	ent := e.ifaces[paths.MainInterface(e.pathSnapshotLocked())]
	e.mu.Unlock()
	if ent == nil {
		return nil, errors.New("no connection")
	}
	return e.vpn.PIARegions(ent.binding.HTTPClient())
}

// removeTunnel takes an interface's tunnel down and forgets it, with the port
// typed in for it.
func (e *Engine) removeTunnel(ifaceID string) error {
	err := e.vpn.Remove(ifaceID)
	e.mu.Lock()
	had := e.settings.Interfaces[ifaceID] != nil && e.settings.Interfaces[ifaceID].ForwardedPort != 0
	e.mu.Unlock()
	if had {
		e.applySettings(func(s *Settings) { s.Interfaces[ifaceID].ForwardedPort = 0 })
	}
	return err
}

// setForwardedPort records the port a VPN provider forwards to an interface's
// imported tunnel (0 clears it). The tunnel's torrent listener moves to it.
func (e *Engine) setForwardedPort(ifaceID string, port int) error {
	if port < 0 || port > 65535 {
		return errors.New("a port is a number from 1 to 65535")
	}
	st, ok := e.tunnelStatus(ifaceID)
	if !ok {
		return errors.New("this connection has no imported VPN tunnel")
	}
	if st.Provider == "pia" {
		return errors.New("PIA forwards its own port")
	}
	e.applySettings(func(s *Settings) {
		is := s.Interfaces[ifaceID]
		if is == nil {
			is = &IfaceSettings{}
			s.Interfaces[ifaceID] = is
		}
		is.ForwardedPort = port
	})
	return nil
}
