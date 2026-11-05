package engine

import (
	"encoding/hex"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"downloadmanagersquared/engine/internal/atomicfile"
	"downloadmanagersquared/engine/internal/netif"
	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/pia"
	"downloadmanagersquared/engine/internal/socks"
	"downloadmanagersquared/engine/internal/tunnel"
)

const (
	tunnelRetry = 30 * time.Second // after a tunnel fails to start or loses its server
	tunnelGrace = 30 * time.Second // for a new tunnel's first handshake
)

// tunnelEntry is the tunnel configured for one interface.
type tunnelEntry struct {
	pia      bool          // PIA makes a new config (and key) at each start
	server   string        // PIA: the server's name, kept apart from other tunnels'
	cfg      tunnel.Config // a .conf's, or PIA's latest
	socks    *socks.Server // landing pages resolved through the tunnel
	t        *tunnel.Tunnel
	on       *netif.Binding // what t runs over
	client   *http.Client
	up       bool
	gen      int // counts starts: torrent sockets of an older one are on a dead stack
	starting bool
	started  time.Time
	retryAt  time.Time
	err      string

	// PIA port forwarding, for this tunnel's server.
	link    piaLink
	sig     *pia.Signature
	pfPort  int // forwarded to the tunnel, 0 if none
	boundAt time.Time
	pfTried time.Time
	pfErr   string
	pfBusy  bool
}

// The .conf files hold private keys, so they live in the engine's folder and
// never in the settings the extension gets.
func (e *Engine) tunnelFile(ifaceID string) string {
	return filepath.Join(e.dir, "tunnels", hex.EncodeToString([]byte(ifaceID))+".conf")
}

func (e *Engine) loadTunnels() {
	files, _ := filepath.Glob(filepath.Join(e.dir, "tunnels", "*.conf"))
	for _, f := range files {
		id, err := hex.DecodeString(strings.TrimSuffix(filepath.Base(f), ".conf"))
		if err != nil {
			continue
		}
		b, err := os.ReadFile(f)
		if err == nil {
			_, err = tunnel.ParseConf(string(b))
		}
		if err != nil && !errors.Is(err, tunnel.ErrNoDNS) { // that one shows on the tunnel
			log.Printf("tunnel for %s unreadable, set aside: %v", id, err)
			setAside(f)
			name := string(id)
			if s := e.settings.Interfaces[name]; s != nil && s.Name != "" {
				name = s.Name
			}
			notify("VPN config unreadable", "VPN config for "+name+" couldn't be read, import it again.", nil)
			continue
		}
		if err := e.setTunnel(string(id), string(b), false); err != nil {
			log.Printf("tunnel for %s: %v", id, err)
		}
	}
}

// setTunnel makes conf the tunnel of an interface, replacing any it had. A
// saved config without DNS (from before that was required) is kept, its tunnel
// down saying why.
func (e *Engine) setTunnel(ifaceID, conf string, save bool) error {
	cfg, err := tunnel.ParseConf(conf)
	var cantRun string
	if errors.Is(err, tunnel.ErrNoDNS) && !save {
		cantRun, err = err.Error(), nil
	}
	if err != nil {
		return err
	}
	if save {
		if err := atomicfile.Write(e.tunnelFile(ifaceID), []byte(conf), 0o600); err != nil {
			return err
		}
	}
	e.removeTunnelEntry(ifaceID)
	srv, err := socks.Listen(e.socksPath(paths.Tunnel(ifaceID)))
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.tunnels[ifaceID] = &tunnelEntry{cfg: cfg, socks: srv, err: cantRun}
	e.mu.Unlock()
	return nil
}

// tunnelWithKey is the name of another interface whose imported config has
// the same private key as conf ("" if none, or conf doesn't parse).
func (e *Engine) tunnelWithKey(ifaceID, conf string) string {
	cfg, err := tunnel.ParseConf(conf)
	if err != nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, te := range e.tunnels {
		if id == ifaceID || te.pia || te.cfg.PrivateKey != cfg.PrivateKey {
			continue
		}
		if ent := e.ifaces[id]; ent != nil && ent.info.Name != "" {
			return ent.info.Name
		}
		if s := e.settings.Interfaces[id]; s != nil && s.Name != "" {
			return s.Name
		}
		return "Another connection"
	}
	return ""
}

// removeTunnel takes an interface's tunnel down and forgets it.
func (e *Engine) removeTunnel(ifaceID string) error {
	e.removeTunnelEntry(ifaceID)
	if err := os.Remove(e.tunnelFile(ifaceID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (e *Engine) removeTunnelEntry(ifaceID string) {
	e.mu.Lock()
	te := e.tunnels[ifaceID]
	delete(e.tunnels, ifaceID)
	e.mu.Unlock()
	if te != nil {
		te.socks.Close()
		if te.t != nil {
			te.t.Close()
		}
	}
}

// syncTunnels runs a tunnel for each interface that has one and is usable,
// following the interface's address, and restarts a tunnel whose server stopped
// answering, like an unreachable interface retrying.
func (e *Engine) syncTunnels(now time.Time) {
	type start struct {
		id string
		te *tunnelEntry
		b  *netif.Binding
	}
	e.mu.Lock()
	wanted := e.piaWantedLocked()
	e.mu.Unlock()
	for _, id := range wanted {
		srv, err := socks.Listen(e.socksPath(paths.Tunnel(id)))
		if err != nil {
			continue
		}
		e.mu.Lock()
		if e.tunnels[id] == nil {
			e.tunnels[id] = &tunnelEntry{pia: true, socks: srv}
			srv = nil
		}
		e.mu.Unlock()
		if srv != nil {
			srv.Close()
		}
	}

	var starts []start
	var stops []*tunnel.Tunnel
	e.mu.Lock()
	for id, te := range e.tunnels {
		ent := e.ifaces[id]
		usable := ent != nil && ent.present && e.enabledLocked(ent)
		if te.t != nil {
			te.up = te.t.Up()
			lost := !te.up && now.Sub(te.started) > tunnelGrace
			if !usable || te.on != ent.binding || lost {
				if lost {
					te.err, te.retryAt = "the VPN server doesn't answer", now.Add(tunnelRetry)
					if !te.pia { // PIA gets a new key each start; a .conf's can expire or be revoked
						te.err += ". If this keeps happening, the config may have expired or been revoked at your provider: make a new one and import it again"
					}
				}
				stops = append(stops, te.t)
				te.t, te.on, te.client, te.up = nil, nil, nil, false
			}
		}
		waitsForUser := te.pia && (e.pia == nil || e.pia.refused)
		cantRun := !te.pia && len(te.cfg.DNS) == 0 // its error says why
		if usable && te.t == nil && !te.starting && !now.Before(te.retryAt) && !waitsForUser && !cantRun {
			te.starting = true
			starts = append(starts, start{id, te, ent.binding})
		}
	}
	e.mu.Unlock()
	for _, t := range stops {
		t.Close()
	}
	for _, s := range starts {
		go e.startTunnel(s.id, s.te, s.b)
	}
}

func (e *Engine) startTunnel(id string, te *tunnelEntry, b *netif.Binding) {
	e.mu.Lock()
	cfg := te.cfg
	e.mu.Unlock()
	var err error
	var link piaLink
	if te.pia {
		// A new key each start: one PIA rejected or forgot is replaced this way.
		cfg, link, err = e.piaConfig(te, b)
	}
	var t *tunnel.Tunnel
	if err == nil {
		t, err = tunnel.Start(cfg, func() (net.PacketConn, error) { return b.ListenPacket(0) }, b.LookupIP)
	}
	if err == nil {
		t.Shake()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err == nil {
		te.cfg = cfg
		// A new server: the old forwarded port is gone with the old tunnel.
		te.link, te.sig, te.pfPort, te.boundAt, te.pfErr = link, nil, 0, time.Time{}, ""
	}
	te.starting = false
	if e.tunnels[id] != te { // removed or replaced meanwhile
		if t != nil {
			t.Close()
		}
		return
	}
	if err != nil {
		log.Printf("tunnel for %s: %v", id, err)
		te.err, te.retryAt = err.Error(), time.Now().Add(tunnelRetry)
		return
	}
	te.t, te.on, te.started, te.err = t, b, time.Now(), ""
	te.gen++
	te.client = netif.NewHTTPClient(t.DialContext)
}

// tunnelUpLocked is the running tunnel of an interface, nil unless it is up
// and the interface is usable.
func (e *Engine) tunnelUpLocked(ifaceID string) *tunnelEntry {
	te := e.tunnels[ifaceID]
	ent := e.ifaces[ifaceID]
	if te == nil || te.t == nil || !te.up || ent == nil || !ent.present || !e.enabledLocked(ent) {
		return nil
	}
	return te
}

func (e *Engine) tunnelViewsLocked(speeds map[string]float64) []IfaceView {
	var out []IfaceView
	for id, te := range e.tunnels {
		ent := e.ifaces[id]
		name := id
		if ent != nil {
			name = ent.info.Name
		} else if s := e.settings.Interfaces[id]; s != nil && s.Name != "" {
			name = s.Name
		}
		state := "connecting"
		switch {
		case te.t != nil && te.up:
			state = "up"
		case ent == nil || !ent.present || !e.enabledLocked(ent):
			state = "off"
		case te.err != "" && te.t == nil:
			state = "down"
		}
		v := IfaceView{
			ID: paths.Tunnel(id), Name: "VPN via " + name, Tunnel: true, Of: id,
			Present: state == "up", Enabled: true, Connections: e.connsLocked(id),
			SocksPort: te.socks.Port(), Speed: speeds[paths.Tunnel(id)],
			TunnelState: state, TunnelError: te.err, Provider: "conf",
		}
		if te.pia {
			v.Provider = "pia"
		}
		if te.cfg.Address.IsValid() {
			v.IP = te.cfg.Address.String()
		}
		if state == "up" {
			switch {
			case !te.pia || !te.link.forwards:
				v.Incoming = "outgoing"
			case te.pfPort != 0 && time.Since(te.boundAt) < pfShown:
				v.Incoming = "reachable"
			case te.pfErr != "":
				v.Incoming = "outgoing"
			}
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

func (e *Engine) closeTunnels() {
	e.mu.Lock()
	ids := make([]string, 0, len(e.tunnels))
	for id := range e.tunnels {
		ids = append(ids, id)
	}
	e.mu.Unlock()
	for _, id := range ids {
		e.removeTunnelEntry(id)
	}
}
