package netif

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/rate"
)

// Lists this machine's real interfaces and, through each one's own DNS and
// binding, fetches the public IP it reaches the internet with. Interfaces on
// different routers must come out at different public IPs: if they don't,
// traffic left through one connection whatever the binding asked for, which no
// test on loopback can see. (Two routers chained behind one upstream would also
// share an IP; unplug one to tell.)
//
// It needs this machine's connections, so it runs only when asked:
//
//	$env:DMS_LIVE_NET = "1"; go test ./internal/netif -run TestListAndBind -v
func TestListAndBind(t *testing.T) {
	if os.Getenv("DMS_LIVE_NET") != "1" {
		t.Skip("set DMS_LIVE_NET=1 to test this machine's real connections")
	}
	list, _, err := List(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 {
		t.Fatal("no interface listed; this machine is online, so at least one should be")
	}
	defaults := 0
	type seen struct{ name, gateway, ip string }
	var out []seen
	for _, i := range list {
		t.Logf("%s | %s | ip=%s gw=%s dns=%v vpn=%v default=%v", i.Name, i.Description, i.IPv4, i.Gateway, i.DNS, i.VPN, i.IsDefault)
		if i.IsDefault {
			defaults++
		}
		if i.IPv4 == nil || i.Index == 0 || i.Gateway == nil || i.ID == "" {
			t.Errorf("%s is listed without an address, index, gateway or ID: %+v", i.Name, i)
			continue
		}
		if i.VPN {
			continue // a VPN adapter may be down or route nothing; it isn't used unless switched on
		}
		ip, err := publicIP(t, NewBinding(i, rate.NewShaper()), i)
		if err != nil {
			t.Errorf("%s can't reach the internet through its own binding and DNS: %v", i.Name, err)
			continue
		}
		t.Logf("  public IP via this interface: %s", ip)
		out = append(out, seen{i.Name, i.Gateway.String(), ip})
	}
	for x, a := range out {
		for _, b := range out[x+1:] {
			if a.gateway != b.gateway && a.ip == b.ip {
				t.Errorf("%s and %s are on different routers but both came out at %s: the binding didn't choose the route", a.name, b.name, a.ip)
			}
		}
	}
	if defaults > 1 {
		t.Errorf("%d interfaces marked as carrying the default route, want at most one", defaults)
	}
}

// publicIP fetches https://api.ipify.org through b, resolving its name through
// i's DNS, and checks the connection left from i's own address.
func publicIP(t *testing.T, b *Binding, i Interface) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if ips, err := b.LookupIP(ctx, "api.ipify.org"); err != nil || len(ips) == 0 {
		return "", fmt.Errorf("its DNS (%v) can't resolve api.ipify.org: %v", i.DNS, err)
	}
	c, err := b.DialContext(ctx, "tcp", "api.ipify.org:443")
	if err != nil {
		return "", err
	}
	local := c.LocalAddr().(*net.TCPAddr).IP
	c.Close()
	if !local.Equal(i.IPv4) {
		return "", fmt.Errorf("connected from %s, not its own address %s", local, i.IPv4)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org", nil)
	resp, err := b.HTTPClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(strings.TrimSpace(string(body)))
	if ip == nil {
		return "", fmt.Errorf("api.ipify.org answered %q, not an IP", body)
	}
	return ip.String(), nil
}

// A binding's connections leave from its interface's address, so a server that
// ties a link to an IP sees the right one. Loopback answers on all of
// 127.0.0.0/8, which gives two addresses to tell apart without a second adapter.
func TestBindingSendsFromItsOwnAddress(t *testing.T) {
	var from atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		from.Store(host)
	}))
	defer srv.Close()
	for _, ip := range []net.IP{net.IPv4(127, 0, 0, 1), net.IPv4(127, 0, 0, 2)} {
		lo := loopback(t)
		lo.IPv4 = ip
		b := NewBinding(lo, rate.NewShaper())
		resp, err := b.HTTPClient().Get(srv.URL)
		if err != nil {
			t.Fatalf("via %s: %v", ip, err)
		}
		resp.Body.Close()
		b.Close()
		if got := from.Load(); got != ip.String() {
			t.Errorf("binding on %s reached the server from %v", ip, got)
		}

		pc, err := b.ListenPacket(0)
		if err != nil {
			t.Fatalf("UDP on %s: %v", ip, err)
		}
		if got := pc.LocalAddr().(*net.UDPAddr).IP; !got.Equal(ip) {
			t.Errorf("UDP socket for %s is on %s", ip, got)
		}
		pc.Close()
	}
}

// loopback is this machine's loopback interface, as List would describe it.
func loopback(t *testing.T) Interface {
	t.Helper()
	ifs, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 {
			return Interface{ID: "lo", Name: i.Name, Index: uint32(i.Index), IPv4: net.IPv4(127, 0, 0, 1)}
		}
	}
	t.Skip("no loopback interface")
	return Interface{}
}

func TestBindingHoldsHTTPToItsShapersLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 300<<10))
	}))
	defer srv.Close()
	s := rate.NewShaper()
	s.SetLimits(0, 200<<10)
	b := NewBinding(loopback(t), s)
	defer b.Close()
	start := time.Now()
	resp, err := b.HTTPClient().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if took := time.Since(start); n != 300<<10 || took < time.Second {
		t.Fatalf("300 KB at 200 KB/s: got %d bytes in %v", n, took)
	}
}

func TestBindingHoldsPacketsToItsShapersLimit(t *testing.T) {
	s := rate.NewShaper()
	s.SetLimits(100<<10, 0)
	pc, err := NewBinding(loopback(t), s).ListenPacket(0)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	start := time.Now()
	for range 200 {
		pc.WriteTo(make([]byte, 1000), pc.LocalAddr())
	}
	if took := time.Since(start); took < 1500*time.Millisecond {
		t.Fatalf("200 KB of packets at 100 KB/s took %v", took)
	}
}
