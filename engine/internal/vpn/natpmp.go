package vpn

import (
	"context"
	"errors"
	"log"
	"net"
	"net/netip"
	"time"

	"downloadmanagersquared/engine/internal/portmap"
	"downloadmanagersquared/engine/internal/tunnel"
)

// NAT-PMP port forwarding on imported tunnels: a VPN server that forwards ports
// this way (Proton VPN) answers on the tunnel's gateway, with no login.
const (
	pmpLifetime = time.Minute      // asked for; Proton's documented loop asks for 60 s
	pmpQuick    = 5 * time.Second  // retry while the lease still runs
	pmpTimeout  = 10 * time.Second // one round of requests
)

// pmpState is an imported tunnel's NAT-PMP forwarding, for one tunnel start.
type pmpState struct {
	gen     int    // the tunnel start (entry.gen) it belongs to
	probed  bool   // the gateway was asked whether it speaks NAT-PMP
	gw      net.IP // the gateway that answered; nil: none did, outgoing only until a restart
	until   time.Time
	renewAt time.Time
	busy    bool
	err     string
	warned  bool // UDP's trouble was logged for this start
}

// forwardPMP starts a job for each imported tunnel that is up and due: a first
// probe, or a renewal. Each runs on its own, so a slow tunnel delays no other.
func (t *Tunnels) forwardPMP(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, te := range t.tunnels {
		// PIA forwards its own way, and a port the user typed in wins.
		if te.pia || t.manualPortLocked(id, te) != 0 || t.upLocked(id) == nil {
			continue
		}
		if te.pmp.gen != te.gen {
			te.pmp = pmpState{gen: te.gen}
		}
		p := &te.pmp
		if p.busy || (p.probed && p.gw == nil) || now.Before(p.renewAt) {
			continue
		}
		p.busy = true
		tn, gen := te.t, te.gen
		t.goJob(func() { t.pmpJob(now, id, te, tn, gen) })
	}
}

func (t *Tunnels) pmpJob(now time.Time, id string, te *entry, tn *tunnel.Tunnel, gen int) {
	t.mu.Lock()
	p := te.pmp
	hint := te.pfPort
	cfg := te.cfg
	t.mu.Unlock()
	gw := func(ip net.IP) portmap.Gateway {
		return portmap.Gateway{IP: ip, Local: tn.Address(), Listen: func() (net.PacketConn, error) { return tn.ListenPacket(0) }}
	}
	ctx, cancel := context.WithTimeout(t.ctx, pmpTimeout)
	defer cancel()

	if !p.probed {
		p.probed = true
		for _, ip := range pmpGateways(cfg, t.gatewayOK) {
			if _, err := portmap.ExternalAddress(ctx, gw(ip)); err == nil {
				p.gw = ip
				break
			}
		}
		if p.gw == nil {
			log.Printf("tunnel for %s: no NAT-PMP on its gateway, outgoing peers only", id)
		}
	}
	var port int
	var err error
	if p.gw != nil {
		if hint == 0 {
			hint = 1 // as Proton's documented requests
		}
		var tcp portmap.Mapping
		tcp, err = portmap.MapPMP(ctx, gw(p.gw), portmap.TCP, 0, hint, pmpLifetime)
		if err == nil && tcp.Lifetime <= 0 {
			err = errors.New("the gateway granted a lease of 0 s")
		}
		if err == nil {
			port = tcp.Public
			udp, uerr := portmap.MapPMP(ctx, gw(p.gw), portmap.UDP, 0, port, pmpLifetime)
			if !p.warned && (uerr != nil || udp.Public != port || (tcp.Internal != 0 && tcp.Internal != port)) {
				p.warned = true
				log.Printf("tunnel for %s: NAT-PMP TCP %d (internal %d), UDP %+v %v; listening on %d", id, port, tcp.Internal, udp, uerr, port)
			}
			p.until, p.renewAt, p.err = now.Add(tcp.Lifetime), now.Add(tcp.Lifetime/2), ""
		} else {
			p.err = err.Error()
			p.renewAt = now.Add(pfRetry)
			if now.Before(p.until) {
				p.renewAt = now.Add(pmpQuick)
			}
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if te.t != tn || te.gen != gen || t.tunnels[id] != te { // restarted or removed meanwhile
		return
	}
	p.busy = false
	te.pmp = p
	if err != nil {
		log.Printf("tunnel for %s: NAT-PMP: %v", id, err)
	}
	if port != 0 {
		te.pfPort = port // the engine's next tick moves the tunnel's torrent sockets to it
	}
}

// pmpGateways are where an imported tunnel's NAT-PMP may answer: its DNS server
// (Proton's is its gateway, 10.2.0.1), then the .1 of its own address. Only
// those ok allows: a VPN's gateway is inside its network, and a public DNS
// server in a .conf (1.1.1.1) is someone else's, with no business getting these.
func pmpGateways(cfg tunnel.Config, ok func(netip.Addr) bool) []net.IP {
	var cands []netip.Addr
	if len(cfg.DNS) > 0 {
		cands = append(cands, cfg.DNS[0])
	}
	if cfg.Address.Is4() {
		a := cfg.Address.As4()
		a[3] = 1
		cands = append(cands, netip.AddrFrom4(a))
	}
	var out []net.IP
	for i, c := range cands {
		if c.Is4() && ok(c) && (i == 0 || c != cands[0]) {
			out = append(out, net.IP(c.AsSlice()))
		}
	}
	return out
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// privateGateway: 10/8, 172.16/12, 192.168/16, or carrier-grade NAT's 100.64/10
// (where some VPNs put their tunnel networks).
func privateGateway(addr netip.Addr) bool {
	return addr.IsPrivate() || cgnat.Contains(addr)
}

// pmpReachableLocked is whether an imported tunnel's NAT-PMP lease runs at now.
func pmpReachableLocked(te *entry, now time.Time) bool {
	return te.pmp.gen == te.gen && te.pmp.gw != nil && te.pfPort != 0 && now.Before(te.pmp.until)
}
