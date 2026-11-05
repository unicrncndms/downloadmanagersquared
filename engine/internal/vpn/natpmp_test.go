package vpn

import (
	"fmt"
	"net/netip"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/tunnel"
	"downloadmanagersquared/engine/internal/vpn/vpntest"
)

// pmpTunnel is a rig with an imported tunnel on lo, to a server whose NAT-PMP
// answers as set. It isn't up yet.
func pmpTunnel(t *testing.T, set func(*vpntest.PMP)) (*rig, *vpntest.Server, *vpntest.PMP) {
	t.Helper()
	r := newRig(t, Options{GatewayOK: vpntest.AnyGateway})
	priv, pub, _ := tunnel.GenerateKey()
	srv := vpntest.NewServer(t, pub)
	f := vpntest.ServePMP(t, srv)
	if set != nil {
		f.Set(set)
	}
	if err := r.Import("lo", srv.Conf(priv)); err != nil {
		t.Fatal(err)
	}
	return r, srv, f
}

// upWithoutWaiting ticks until lo's tunnel is up, returning while the work
// that tick started (its NAT-PMP probe) still runs.
func (r *rig) upWithoutWaiting(t *testing.T) {
	t.Helper()
	waitFor(t, "the tunnel to come up", 30*time.Second, func() bool {
		r.Wait()
		r.Tick(time.Now(), r.ifaces)
		_, ok := r.Up("lo")
		return ok
	})
}

func TestImportedTunnelForwardsAPortByNATPMPAsSoonAsItIsUp(t *testing.T) {
	r, _, f := pmpTunnel(t, nil)
	r.waitUp(t)
	if s := r.status(t, "lo", time.Now()); s.Incoming != "reachable" {
		t.Fatalf("tunnel with a NAT-PMP port shown as %q", s.Incoming)
	}
	if tcp, udp, probes := f.Counts(); tcp != 1 || udp != 1 || probes != 1 {
		t.Fatalf("mapped TCP %d and UDP %d times after %d probes, want once each", tcp, udp, probes)
	}
	if up, _ := r.Up("lo"); up.Port != 51234 {
		t.Fatalf("torrents would listen on %d, want 51234", up.Port)
	}
}

func TestNATPMPLeaseIsRenewedAtHalfItsLifeForAnHourOnOnePort(t *testing.T) {
	r, _, f := pmpTunnel(t, nil)
	r.waitUp(t)
	start := time.Now()
	r.tick(start.Add(20 * time.Second))
	if tcp, _, _ := f.Counts(); tcp != 1 {
		t.Fatalf("renewed after 20 s of a 60 s lease (%d mappings)", tcp)
	}
	for i := 1; i <= 120; i++ { // an hour of 30 s steps
		r.tick(start.Add(time.Duration(i) * 30 * time.Second))
	}
	if tcp, _, probes := f.Counts(); tcp != 121 || probes != 1 {
		t.Fatalf("an hour later: %d TCP mappings, %d probes; want one every 30 s after a single probe", tcp, probes)
	}
	at := start.Add(time.Hour + time.Second)
	if up, _ := r.Up("lo"); up.Port != 51234 || r.status(t, "lo", at).Incoming != "reachable" {
		t.Fatalf("after an hour: port %d, %q", up.Port, r.status(t, "lo", at).Incoming)
	}
	// Unrenewed, the port is gone with its lease.
	if r.status(t, "lo", start.Add(time.Hour+61*time.Second)).Incoming == "reachable" {
		t.Fatal("still reachable after the lease ran out")
	}
}

func TestShorterGrantedLeaseIsRenewedSooner(t *testing.T) {
	r, _, f := pmpTunnel(t, func(f *vpntest.PMP) { f.Lifetime = 20 })
	r.waitUp(t)
	r.tick(time.Now().Add(11 * time.Second))
	if tcp, _, _ := f.Counts(); tcp != 2 {
		t.Fatalf("a 20 s lease was mapped %d times in 11 s, want renewed at 10 s", tcp)
	}
}

func TestLostNATPMPRenewalIsRetriedWithinTheLease(t *testing.T) {
	r, _, f := pmpTunnel(t, nil)
	r.waitUp(t)
	start := time.Now()

	f.Set(func(f *vpntest.PMP) { f.Drop = 1 }) // one packet: retried within the request
	r.tick(start.Add(30 * time.Second))
	if tcp, _, _ := f.Counts(); tcp != 2 || r.status(t, "lo", start.Add(89*time.Second)).Incoming != "reachable" {
		t.Fatalf("a lost packet cost the renewal: %d mappings", tcp)
	}

	f.Set(func(f *vpntest.PMP) { f.Drop = 3 }) // a whole round: retried in 5 s, still inside the lease
	r.tick(start.Add(60 * time.Second))
	r.tick(start.Add(65 * time.Second))
	if tcp, _, _ := f.Counts(); tcp != 3 || r.status(t, "lo", start.Add(124*time.Second)).Incoming != "reachable" {
		t.Fatalf("a lost round wasn't retried within the lease: %d mappings", tcp)
	}
}

func TestAChangedNATPMPPortIsTheOneToListenOn(t *testing.T) {
	r, _, f := pmpTunnel(t, nil)
	r.waitUp(t)
	f.Set(func(f *vpntest.PMP) { f.Public = 52000 })
	r.tick(time.Now().Add(30 * time.Second))
	if up, _ := r.Up("lo"); up.Port != 52000 {
		t.Fatalf("after the port changed torrents would listen on %d, want 52000", up.Port)
	}
}

func TestGatewayWithoutNATPMPIsOutgoingOnlyAndNotAskedAgain(t *testing.T) {
	r, _, f := pmpTunnel(t, func(f *vpntest.PMP) { f.Silent = true })
	r.upWithoutWaiting(t)
	if s := r.status(t, "lo", time.Now()); s.Incoming != "" {
		t.Fatalf("while its gateway is being asked the tunnel shows %q, want not known", s.Incoming)
	}
	begin := time.Now()
	r.Wait()
	if d := time.Since(begin); d > 5*time.Second { // two gateways tried, about 2 s each
		t.Fatalf("finding no NAT-PMP took %v", d)
	}
	if s := r.status(t, "lo", time.Now()); s.Incoming != "outgoing" {
		t.Fatalf("tunnel without NAT-PMP shown as %q", s.Incoming)
	}
	f.Set(func(f *vpntest.PMP) { f.Silent = false })
	r.tick(time.Now().Add(time.Hour))
	if _, _, probes := f.Counts(); probes != 0 {
		t.Fatal("asked again before the tunnel restarted")
	}
	if up, _ := r.Up("lo"); up.Port != 0 {
		t.Fatalf("torrents on a tunnel without NAT-PMP would listen on %d", up.Port)
	}
}

func TestRefusedUDPStillForwardsTCP(t *testing.T) {
	r, _, _ := pmpTunnel(t, func(f *vpntest.PMP) { f.RefuseUDP = true })
	r.waitUp(t)
	up, _ := r.Up("lo")
	if s := r.status(t, "lo", time.Now()); up.Port != 51234 || s.Incoming != "reachable" {
		t.Fatalf("with UDP refused: port %d, %q", up.Port, s.Incoming)
	}
}

func TestATunnelRestartAsksNATPMPAgain(t *testing.T) {
	r, _, f := pmpTunnel(t, nil)
	r.waitUp(t)
	restart := func() {
		r.iface("lo").Usable = false
		r.tick(time.Now())
		r.iface("lo").Usable = true
		r.waitUp(t)
	}
	f.Set(func(f *vpntest.PMP) { f.Silent = true })
	restart()
	if up, _ := r.Up("lo"); up.Port != 0 || r.status(t, "lo", time.Now()).Incoming == "reachable" {
		t.Fatalf("a new tunnel start kept the old one's port %d", up.Port)
	}
	f.Set(func(f *vpntest.PMP) { f.Silent = false })
	restart()
	if up, _ := r.Up("lo"); up.Port != 51234 {
		t.Fatalf("after a restart: port %d", up.Port)
	}
	if _, _, probes := f.Counts(); probes != 2 {
		t.Fatalf("%d probes answered, want one per start", probes)
	}
}

// A tunnel whose gateway is slow to answer mustn't hold up another's renewal.
func TestASlowTunnelDelaysNoOtherNATPMPRenewal(t *testing.T) {
	r := newRig(t, Options{GatewayOK: vpntest.AnyGateway}, "lo", "lo2")
	fakes := map[string]*vpntest.PMP{}
	for _, id := range []string{"lo", "lo2"} {
		priv, pub, _ := tunnel.GenerateKey()
		srv := vpntest.NewServer(t, pub)
		fakes[id] = vpntest.ServePMP(t, srv)
		if err := r.Import(id, srv.Conf(priv)); err != nil {
			t.Fatal(err)
		}
	}
	r.waitUp(t, "lo", "lo2")
	fakes["lo"].Set(func(f *vpntest.PMP) { f.Drop = 1000 })
	begin := time.Now()
	r.Tick(begin.Add(30*time.Second), r.ifaces) // both renewals due
	waitFor(t, "the other tunnel's renewal", 1500*time.Millisecond, func() bool {
		tcp, _, _ := fakes["lo2"].Counts()
		return tcp == 2
	})
	r.Wait()
	if d := time.Since(begin); d < 1500*time.Millisecond {
		t.Fatalf("the unanswered renewal was given up on after %v: the test proves nothing", d)
	}
}

func TestNATPMPIsOnlySentToPrivateGateways(t *testing.T) {
	gws := func(addr, dns string) []string {
		cfg := tunnel.Config{Address: netip.MustParseAddr(addr)}
		if dns != "" {
			cfg.DNS = []netip.Addr{netip.MustParseAddr(dns)}
		}
		var out []string
		for _, ip := range pmpGateways(cfg, privateGateway) {
			out = append(out, ip.String())
		}
		return out
	}
	for _, c := range []struct {
		addr, dns string
		want      string
	}{
		{"10.2.0.2", "10.2.0.1", "[10.2.0.1]"},                // Proton: DNS is the gateway, and the .1 is the same
		{"10.64.12.7", "10.64.0.1", "[10.64.0.1 10.64.12.1]"}, // Mullvad-like: both asked
		{"10.9.0.2", "1.1.1.1", "[10.9.0.1]"},                 // a public resolver is skipped
		{"100.72.3.4", "", "[100.72.3.1]"},                    // carrier-grade NAT range counts as private
		{"203.0.113.5", "8.8.8.8", "[]"},                      // nothing private: nothing asked
	} {
		if got := fmt.Sprint(gws(c.addr, c.dns)); got != c.want {
			t.Errorf("address %s, DNS %q: gateways %s, want %s", c.addr, c.dns, got, c.want)
		}
	}
}

func TestATypedInPortWinsAndNATPMPIsNeverAsked(t *testing.T) {
	r, _, f := pmpTunnel(t, nil) // its gateway would answer NAT-PMP
	if err := r.SetForwardedPort("lo", 41000); err != nil {
		t.Fatal(err)
	}
	r.waitUp(t)
	r.tick(time.Now())
	if tcp, udp, probes := f.Counts(); tcp+udp+probes != 0 {
		t.Fatalf("NAT-PMP asked (TCP %d, UDP %d, probes %d) despite a typed-in port", tcp, udp, probes)
	}
	s := r.status(t, "lo", time.Now())
	if s.Incoming != "forwarded" || s.ForwardedPort != 41000 {
		t.Fatalf("tunnel shown as incoming %q, port %d; want forwarded, 41000", s.Incoming, s.ForwardedPort)
	}
	if up, _ := r.Up("lo"); up.Port != 41000 {
		t.Fatalf("torrents would listen on %d, want 41000", up.Port)
	}

	// Cleared, the tunnel is NAT-PMP's again.
	if err := r.SetForwardedPort("lo", 0); err != nil {
		t.Fatal(err)
	}
	r.tick(time.Now())
	s = r.status(t, "lo", time.Now())
	if up, _ := r.Up("lo"); up.Port != 51234 || s.Incoming != "reachable" || s.ForwardedPort != 0 {
		t.Fatalf("after clearing the port: listening on %d, %q, port %d; want NAT-PMP's 51234", up.Port, s.Incoming, s.ForwardedPort)
	}
}
