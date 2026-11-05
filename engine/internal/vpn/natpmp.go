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

// pmpForwarder is NAT-PMP forwarding for one start of an imported tunnel.
type pmpForwarder struct {
	t    *Tunnels
	id   string
	tn   *tunnel.Tunnel
	gws  []net.IP // where to ask
	busy bool
	st   pmpState
}

// pmpState is what a round of NAT-PMP requests learns.
type pmpState struct {
	probed  bool   // the gateway was asked whether it speaks NAT-PMP
	gw      net.IP // the gateway that answered; nil: none did, outgoing only until a restart
	port    int
	until   time.Time
	renewAt time.Time
	err     string
	warned  bool // UDP's trouble was logged for this start
}

func (p *imported) newPMP(id string, tn *tunnel.Tunnel, cfg tunnel.Config) forwarder {
	return &pmpForwarder{t: p.t, id: id, tn: tn, gws: pmpGateways(cfg, p.gatewayOK)}
}

// forward probes the gateway once, then renews the lease at half its life.
// Each tunnel's round runs on its own, so a slow one delays no other.
func (f *pmpForwarder) forward(now time.Time) {
	if f.busy || (f.st.probed && f.st.gw == nil) || now.Before(f.st.renewAt) {
		return
	}
	f.busy = true
	st := f.st
	f.t.goJob(func() {
		st := f.round(now, st)
		f.t.mu.Lock()
		f.busy, f.st = false, st
		f.t.mu.Unlock()
	})
}

func (f *pmpForwarder) port(now time.Time) (int, string) {
	switch {
	case f.st.gw != nil && f.st.port != 0 && now.Before(f.st.until):
		return f.st.port, "reachable"
	case !f.st.probed:
		return f.st.port, "" // not known until its gateway is asked
	}
	return f.st.port, "outgoing"
}

// round asks the gateway for a lease, first finding which gateway answers.
func (f *pmpForwarder) round(now time.Time, p pmpState) pmpState {
	gw := func(ip net.IP) portmap.Gateway {
		return portmap.Gateway{IP: ip, Local: f.tn.Address(), Listen: func() (net.PacketConn, error) { return f.tn.ListenPacket(0) }}
	}
	ctx, cancel := context.WithTimeout(f.t.ctx, pmpTimeout)
	defer cancel()

	if !p.probed {
		p.probed = true
		for _, ip := range f.gws {
			if _, err := portmap.ExternalAddress(ctx, gw(ip)); err == nil {
				p.gw = ip
				break
			}
		}
		if p.gw == nil {
			log.Printf("tunnel for %s: no NAT-PMP on its gateway, outgoing peers only", f.id)
			return p
		}
	}
	hint := p.port
	if hint == 0 {
		hint = 1 // as Proton's documented requests
	}
	tcp, err := portmap.MapPMP(ctx, gw(p.gw), portmap.TCP, 0, hint, pmpLifetime)
	if err == nil && tcp.Lifetime <= 0 {
		err = errors.New("the gateway granted a lease of 0 s")
	}
	if err != nil {
		log.Printf("tunnel for %s: NAT-PMP: %v", f.id, err)
		p.err = err.Error()
		p.renewAt = now.Add(pfRetry)
		if now.Before(p.until) {
			p.renewAt = now.Add(pmpQuick)
		}
		return p
	}
	// A changed port reaches torrents at the engine's next tick, through Up.
	p.port = tcp.Public
	udp, uerr := portmap.MapPMP(ctx, gw(p.gw), portmap.UDP, 0, p.port, pmpLifetime)
	if !p.warned && (uerr != nil || udp.Public != p.port || (tcp.Internal != 0 && tcp.Internal != p.port)) {
		p.warned = true
		log.Printf("tunnel for %s: NAT-PMP TCP %d (internal %d), UDP %+v %v; listening on %d", f.id, p.port, tcp.Internal, udp, uerr, p.port)
	}
	p.until, p.renewAt, p.err = now.Add(tcp.Lifetime), now.Add(tcp.Lifetime/2), ""
	return p
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
