package vpn

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/netif"
	"downloadmanagersquared/engine/internal/netif/netiftest"
	"downloadmanagersquared/engine/internal/rate"
	"downloadmanagersquared/engine/internal/tunnel"
	"downloadmanagersquared/engine/internal/vpn/vpntest"
)

// ipv6OnlyLoopback makes the rig's interfaces loopback over IPv6 only, as
// List describes an IPv6-only connection, with ::1 as DNS server.
func ipv6OnlyLoopback(t *testing.T, r *rig) {
	t.Helper()
	if ln, err := net.ListenPacket("udp6", "[::1]:0"); err != nil {
		t.Skip("no IPv6 loopback: ", err)
	} else {
		ln.Close()
	}
	for i := range r.ifaces {
		lo := vpntest.Loopback(r.ifaces[i].ID, r.ifaces[i].Name)
		lo.IPv4, lo.IPv6, lo.DNS = nil, net.IPv6loopback, []net.IP{net.IPv6loopback}
		r.ifaces[i].Binding = netif.NewBinding(lo, rate.NewShaper())
	}
}

// A .conf tunnel whose endpoint is IPv6, an address or a name with only an
// AAAA record, comes up over an IPv6-only connection and carries IPv4 inside.
func TestATunnelWithAnIPv6EndpointRunsOverAnIPv6OnlyInterface(t *testing.T) {
	port, _ := netiftest.DNS(t, nil, net.IPv6loopback)
	old := netif.DNSPort
	netif.DNSPort = port
	t.Cleanup(func() { netif.DNSPort = old })
	for _, byName := range []bool{false, true} {
		r := newRig(t, Options{})
		ipv6OnlyLoopback(t, r)
		priv, pub, _ := tunnel.GenerateKey()
		srv := vpntest.NewServer6(t, pub)
		conf := srv.Conf(priv)
		if byName {
			_, p, _ := net.SplitHostPort(srv.Endpoint)
			conf = strings.Replace(conf, srv.Endpoint, "vpn.example:"+p, 1)
		}
		if !strings.Contains(conf, "Endpoint = [::1]:") && !byName {
			t.Fatalf("endpoint not IPv6: %s", conf)
		}
		if err := r.Import("lo", conf); err != nil {
			t.Fatal(err)
		}
		r.waitUp(t)
		up, _ := r.Up("lo")
		req, _ := http.NewRequest(http.MethodGet, "http://203.0.113.1/vpn.bin", nil)
		req.Header.Set("Range", "bytes=0-9")
		resp, err := up.Client.Do(req)
		if err != nil {
			t.Fatalf("by name %v: %v", byName, err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !bytes.Equal(b, srv.Data[:10]) {
			t.Fatalf("by name %v, through the tunnel: %q", byName, b)
		}
	}
}

// PIA's servers have IPv4 endpoints only: an IPv6-only interface gets no PIA
// tunnel, and its row says why.
func TestPIAIsNotStartedOnAnIPv6OnlyInterface(t *testing.T) {
	f := vpntest.NewPIA(t, false, 1)
	r := piaRig(t, f)
	ipv6OnlyLoopback(t, r)
	r.tick(time.Now())
	if err := r.SetPIA("p0123456", "right"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		r.tick(time.Now())
	}
	if _, ok := r.Up("lo"); ok {
		t.Fatal("a PIA tunnel came up on an IPv6-only interface")
	}
	if s := r.status(t, "lo", time.Now()); s.Err != "PIA needs IPv4" || s.State == "up" || s.State == "connecting" {
		t.Fatalf("status %+v", s)
	}
	if n := f.Keys.Load(); n != 0 {
		t.Errorf("%d keys registered with PIA for it", n)
	}
}
