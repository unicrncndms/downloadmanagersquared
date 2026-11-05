package engine

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/netif"
	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/tunnel"
)

// fakePMP is a VPN server's NAT-PMP, answering inside its tunnel the way
// Proton's is documented to: the public port it picks, for at most 60 s.
type fakePMP struct {
	mu        sync.Mutex
	public    int
	lifetime  uint32
	refuseUDP bool
	silent    bool
	drop      int // mapping requests left to ignore, like lost packets
	tcp, udp  int // mappings granted
	probes    int
}

func serveFakePMP(t *testing.T, srv *vpnServer) *fakePMP {
	t.Helper()
	f := &fakePMP{public: 51234, lifetime: 60}
	pc, err := srv.t.ListenPacket(5351)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 64)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			f.mu.Lock()
			switch {
			case f.silent || n < 2 || buf[0] != 0:
			case buf[1] == 0:
				f.probes++
				pc.WriteTo([]byte{0, 128, 0, 0, 0, 0, 0, 1, 198, 51, 100, 9}, from)
			case n == 12 && (buf[1] == 1 || buf[1] == 2) && f.drop > 0:
				f.drop--
			case n == 12 && (buf[1] == 1 || buf[1] == 2):
				resp := make([]byte, 16)
				resp[1] = 128 + buf[1]
				life := min(binary.BigEndian.Uint32(buf[8:]), f.lifetime)
				if buf[1] == 1 && f.refuseUDP {
					binary.BigEndian.PutUint16(resp[2:], 2)
				} else if buf[1] == 1 {
					f.udp++
				} else {
					f.tcp++
				}
				binary.BigEndian.PutUint16(resp[10:], uint16(f.public))
				binary.BigEndian.PutUint32(resp[12:], life)
				pc.WriteTo(resp, from)
			}
			f.mu.Unlock()
		}
	}()
	return f
}

func (f *fakePMP) set(fn func(f *fakePMP)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}

func (f *fakePMP) counts() (tcp, udp, probes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tcp, f.udp, f.probes
}

// pmpTunnel is an engine with an imported tunnel that is up, to a server whose
// NAT-PMP answers.
func pmpTunnel(t *testing.T) (*Engine, *vpnServer, *fakePMP) {
	t.Helper()
	e := loopbackEngine(t)
	clientPriv, clientPub, _ := tunnel.GenerateKey()
	srv := newVPNServer(t, clientPub)
	f := serveFakePMP(t, srv)
	importConf(t, e, srv, clientPriv)
	waitTunnelUp(t, e)
	return e, srv, f
}

func pmpPort(e *Engine) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tunnels["lo"].pfPort
}

func pmpReachable(e *Engine, at time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return pmpReachableLocked(e.tunnels["lo"], at)
}

func TestImportedTunnelForwardsAPortByNATPMPAndTakesPeersOnIt(t *testing.T) {
	e, srv, f := pmpTunnel(t)
	if v := ifaceView(t, e, paths.Tunnel("lo")); v.Incoming != "" {
		t.Fatalf("before its gateway is asked the tunnel shows %q, want not known", v.Incoming)
	}
	e.forwardPMP(time.Now()).Wait()
	if v := ifaceView(t, e, paths.Tunnel("lo")); v.Incoming != "reachable" {
		t.Fatalf("tunnel with a NAT-PMP port shown as %q", v.Incoming)
	}
	if tcp, udp, _ := f.counts(); tcp != 1 || udp != 1 {
		t.Fatalf("mapped TCP %d and UDP %d times, want once each", tcp, udp)
	}
	e.syncTorrentIfaces()
	vpnTorrentInSwarm(t, e)
	if ifs := e.torrentIfacesFor(paths.VPNOnly); len(ifs) != 1 || ifs[0].Port != 51234 {
		t.Fatalf("VPN-only torrents listen on %+v, want port 51234", ifs)
	}
	c, err := srv.t.DialContext(t.Context(), "tcp4", "10.9.0.2:51234")
	if err != nil {
		t.Fatalf("a peer coming in through the VPN server couldn't connect: %v", err)
	}
	c.Close()
}

func TestNATPMPLeaseIsRenewedAtHalfItsLifeForAnHourOnOnePort(t *testing.T) {
	e, _, f := pmpTunnel(t)
	start := time.Now()
	e.forwardPMP(start).Wait()
	e.forwardPMP(start.Add(20 * time.Second)).Wait()
	if tcp, _, _ := f.counts(); tcp != 1 {
		t.Fatalf("renewed after 20 s of a 60 s lease (%d mappings)", tcp)
	}
	for i := 1; i <= 120; i++ { // an hour of 30 s steps
		e.forwardPMP(start.Add(time.Duration(i) * 30 * time.Second)).Wait()
	}
	if tcp, _, probes := f.counts(); tcp != 121 || probes != 1 {
		t.Fatalf("an hour later: %d TCP mappings, %d probes; want one every 30 s after a single probe", tcp, probes)
	}
	if pmpPort(e) != 51234 || !pmpReachable(e, start.Add(time.Hour+time.Second)) {
		t.Fatalf("after an hour: port %d, reachable %v", pmpPort(e), pmpReachable(e, start.Add(time.Hour+time.Second)))
	}
	// Unrenewed, the port is gone with its lease.
	if pmpReachable(e, start.Add(time.Hour+61*time.Second)) {
		t.Fatal("still reachable after the lease ran out")
	}
}

func TestShorterGrantedLeaseIsRenewedSooner(t *testing.T) {
	e, _, f := pmpTunnel(t)
	f.set(func(f *fakePMP) { f.lifetime = 20 })
	start := time.Now()
	e.forwardPMP(start).Wait()
	e.forwardPMP(start.Add(11 * time.Second)).Wait()
	if tcp, _, _ := f.counts(); tcp != 2 {
		t.Fatalf("a 20 s lease was mapped %d times in 11 s, want renewed at 10 s", tcp)
	}
}

func TestLostNATPMPRenewalIsRetriedWithinTheLease(t *testing.T) {
	e, _, f := pmpTunnel(t)
	start := time.Now()
	e.forwardPMP(start).Wait()

	f.set(func(f *fakePMP) { f.drop = 1 }) // one packet: retried within the request
	e.forwardPMP(start.Add(30 * time.Second)).Wait()
	if tcp, _, _ := f.counts(); tcp != 2 || !pmpReachable(e, start.Add(89*time.Second)) {
		t.Fatalf("a lost packet cost the renewal: %d mappings", tcp)
	}

	f.set(func(f *fakePMP) { f.drop = 3 }) // a whole round: retried in 5 s, still inside the lease
	e.forwardPMP(start.Add(60 * time.Second)).Wait()
	e.forwardPMP(start.Add(65 * time.Second)).Wait()
	if tcp, _, _ := f.counts(); tcp != 3 || !pmpReachable(e, start.Add(124*time.Second)) {
		t.Fatalf("a lost round wasn't retried within the lease: %d mappings", tcp)
	}
}

func TestChangedNATPMPPortMovesTheListener(t *testing.T) {
	e, srv, f := pmpTunnel(t)
	start := time.Now()
	e.forwardPMP(start).Wait()
	e.syncTorrentIfaces()
	vpnTorrentInSwarm(t, e)
	f.set(func(f *fakePMP) { f.public = 52000 })
	e.forwardPMP(start.Add(30 * time.Second)).Wait()
	e.syncTorrentIfaces()
	if ifs := e.torrentIfacesFor(paths.VPNOnly); len(ifs) != 1 || ifs[0].Port != 52000 {
		t.Fatalf("after the port changed torrents listen on %+v, want 52000", ifs)
	}
	waitFor(t, "a peer to connect on the new port", 5*time.Second, func() bool {
		c, err := srv.t.DialContext(t.Context(), "tcp4", "10.9.0.2:52000")
		if err == nil {
			c.Close()
		}
		return err == nil
	})
}

func TestGatewayWithoutNATPMPIsOutgoingOnlyAndNotAskedAgain(t *testing.T) {
	e, _, f := pmpTunnel(t)
	f.set(func(f *fakePMP) { f.silent = true })
	begin := time.Now()
	e.forwardPMP(begin).Wait()
	if d := time.Since(begin); d > 5*time.Second { // two gateways tried, about 2 s each
		t.Fatalf("finding no NAT-PMP took %v", d)
	}
	if v := ifaceView(t, e, paths.Tunnel("lo")); v.Incoming != "outgoing" {
		t.Fatalf("tunnel without NAT-PMP shown as %q", v.Incoming)
	}
	f.set(func(f *fakePMP) { f.silent = false })
	e.forwardPMP(begin.Add(time.Hour)).Wait()
	if _, _, probes := f.counts(); probes != 0 {
		t.Fatal("asked again before the tunnel restarted")
	}
	if ifs := e.torrentIfacesFor(paths.VPNOnly); len(ifs) != 1 || ifs[0].Port != 0 {
		t.Fatalf("torrents on a tunnel without NAT-PMP: %+v", ifs)
	}
}

func TestRefusedUDPStillForwardsTCP(t *testing.T) {
	e, _, f := pmpTunnel(t)
	f.set(func(f *fakePMP) { f.refuseUDP = true })
	e.forwardPMP(time.Now()).Wait()
	if pmpPort(e) != 51234 || ifaceView(t, e, paths.Tunnel("lo")).Incoming != "reachable" {
		t.Fatalf("with UDP refused: port %d, %q", pmpPort(e), ifaceView(t, e, paths.Tunnel("lo")).Incoming)
	}
}

func TestATunnelRestartAsksNATPMPAgain(t *testing.T) {
	e, _, f := pmpTunnel(t)
	e.forwardPMP(time.Now()).Wait()
	e.mu.Lock()
	te := e.tunnels["lo"]
	te.t.Close()
	te.t, te.on, te.client, te.up = nil, nil, nil, false
	e.mu.Unlock()
	waitTunnelUp(t, e)
	if pmpPort(e) != 0 || pmpReachable(e, time.Now()) {
		t.Fatal("a new tunnel start kept the old one's port")
	}
	e.forwardPMP(time.Now()).Wait()
	if _, _, probes := f.counts(); probes != 2 || pmpPort(e) != 51234 {
		t.Fatalf("after a restart: %d probes, port %d", probes, pmpPort(e))
	}
}

// A tunnel whose gateway is slow to give up mustn't hold up another's renewal.
func TestASlowTunnelDelaysNoOtherNATPMPRenewal(t *testing.T) {
	e := loopbackEngine(t)
	lo := loopbackIfaces()[0]
	lo2 := lo
	lo2.ID, lo2.Name = "lo2", "Loopback 2"
	e.mu.Lock()
	e.refreshInterfacesLocked([]netif.Interface{lo, lo2})
	e.mu.Unlock()
	fakes := map[string]*fakePMP{}
	for _, id := range []string{"lo", "lo2"} {
		priv, pub, _ := tunnel.GenerateKey()
		srv := newVPNServer(t, pub)
		fakes[id] = serveFakePMP(t, srv)
		conf := fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.9.0.2/32\nDNS = 203.0.113.1\n[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = %s\n", priv, srv.pub, srv.endpoint)
		p, _ := json.Marshal(map[string]string{"iface": id, "conf": conf})
		if _, err := e.Call(nil, "importTunnel", p); err != nil {
			t.Fatal(err)
		}
	}
	fakes["lo"].set(func(f *fakePMP) { f.silent = true })
	waitFor(t, "both tunnels to come up", 30*time.Second, func() bool {
		e.syncTunnels(time.Now())
		return len(e.Paths(paths.VPNOnly)) == 2
	})
	begin := time.Now()
	wg := e.forwardPMP(begin)
	waitFor(t, "the other tunnel's port", 1500*time.Millisecond, func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return pmpReachableLocked(e.tunnels["lo2"], begin)
	})
	wg.Wait()
	if d := time.Since(begin); d < 1500*time.Millisecond {
		t.Fatalf("the silent gateway was given up on after %v: the test proves nothing", d)
	}
}
