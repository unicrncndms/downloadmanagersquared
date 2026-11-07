package netif

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"downloadmanagersquared/engine/internal/netif/netiftest"
	"downloadmanagersquared/engine/internal/rate"
)

// loopback6 is the loopback interface used over IPv6 only, as List describes an
// IPv6-only interface.
func loopback6(t *testing.T) Interface {
	t.Helper()
	lo := loopback(t)
	lo.IPv4, lo.IPv6 = nil, net.IPv6loopback
	if ln, err := net.Listen("tcp6", "[::1]:0"); err != nil {
		t.Skip("no IPv6 loopback: ", err)
	} else {
		ln.Close()
	}
	return lo
}

// A binding on an IPv6 address dials and listens from that address, pinned to
// its interface with IPV6_UNICAST_IF.
func TestBindingDialsAndListensOverIPv6(t *testing.T) {
	b := NewBinding(loopback6(t), rate.NewShaper())
	defer b.Close()
	ln, err := b.Listen6(0)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if got := ln.Addr().(*net.TCPAddr).IP; !got.Equal(net.IPv6loopback) {
		t.Errorf("listening on %v, want ::1", got)
	}
	go func() {
		if c, err := ln.Accept(); err == nil {
			c.Write([]byte("hi"))
			c.Close()
		}
	}()
	c, err := b.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	if _, err := c.Read(buf); err != nil || string(buf) != "hi" {
		t.Errorf("read %q, %v", buf, err)
	}
	if got := c.LocalAddr().(*net.TCPAddr).IP; !got.Equal(net.IPv6loopback) {
		t.Errorf("dialled from %v, want ::1", got)
	}
	c.Close()

	pc, err := b.ListenPacket6(0)
	if err != nil {
		t.Fatal(err)
	}
	if got := pc.LocalAddr().(*net.UDPAddr).IP; !got.Equal(net.IPv6loopback) {
		t.Errorf("UDP socket on %v, want ::1", got)
	}
	pc.Close()
	if _, err := b.Listen(0); !errors.Is(err, ErrNoIPv4) {
		t.Errorf("an IPv4 listener on an IPv6-only interface: %v", err)
	}
}

// A literal IPv4 address can't be reached from an IPv6-only interface (no
// NAT64 prefix discovery), and the error says why.
func TestALiteralIPv4URLFailsOnAnIPv6OnlyInterface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	b := NewBinding(loopback6(t), rate.NewShaper())
	defer b.Close()
	_, err := b.HTTPClient().Get(srv.URL) // http://127.0.0.1:port
	if !errors.Is(err, ErrNoIPv4) || err.Error() == "" {
		t.Fatalf("got %v, want %q", err, ErrNoIPv4)
	}
	// And the other way round: an IPv4-only interface has no IPv6.
	b4 := NewBinding(loopback(t), rate.NewShaper())
	if _, err := b4.DialContext(context.Background(), "tcp", "[::1]:80"); !errors.Is(err, ErrNoIPv6) {
		t.Errorf("IPv6 from an IPv4-only interface: %v", err)
	}
}

// dnsServer answers A with a and AAAA with aaaa on 127.0.0.1 and ::1, and
// points DNSPort at itself; asked returns the query types it got.
func dnsServer(t *testing.T, a, aaaa net.IP) func() []dnsmessage.Type {
	t.Helper()
	port, asked := netiftest.DNS(t, a, aaaa)
	old := DNSPort
	DNSPort = port
	t.Cleanup(func() { DNSPort = old })
	return asked
}

// Each interface asks only for the families it has, and over the families it
// has: an IPv6 DNS server through its IPv6 address.
func TestLookupAsksOnlyForTheInterfacesFamilies(t *testing.T) {
	asked := dnsServer(t, net.IPv4(192, 0, 2, 1), net.ParseIP("2001:db8::1"))
	lo4 := loopback(t)
	lo4.DNS = []net.IP{net.IPv4(127, 0, 0, 1)}
	lo6 := loopback6(t)
	lo6.DNS = []net.IP{net.IPv6loopback}
	dual := lo4
	dual.IPv6, dual.DNS = net.IPv6loopback, []net.IP{net.IPv6loopback, net.IPv4(127, 0, 0, 1)}
	for _, c := range []struct {
		name  string
		iface Interface
		want  []string
		types []dnsmessage.Type
	}{
		{"IPv4 only", lo4, []string{"192.0.2.1"}, []dnsmessage.Type{dnsmessage.TypeA}},
		{"IPv6 only", lo6, []string{"2001:db8::1"}, []dnsmessage.Type{dnsmessage.TypeAAAA}},
		{"dual-stack", dual, []string{"192.0.2.1", "2001:db8::1"}, []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA}},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ips, err := NewBinding(c.iface, rate.NewShaper()).LookupIP(ctx, "files.example")
		cancel()
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		var got []string
		for _, ip := range ips {
			got = append(got, ip.String())
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s resolved %v, want %v (IPv4 first)", c.name, got, c.want)
		}
		types := asked()
		slices.Sort(types)
		if !slices.Equal(slices.Compact(types), c.types) {
			t.Errorf("%s asked %v, want %v", c.name, types, c.types)
		}
	}
}

// An IPv6-only interface without DNS servers asks public IPv6 resolvers, and
// says so: its carrier's DNS64 is lost.
func TestAnIPv6OnlyInterfaceWithoutDNSFallsBackToPublicIPv6Resolvers(t *testing.T) {
	var logged bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(prev)
	b := NewBinding(Interface{Name: "Phone", IPv6: net.ParseIP("2001:470:1::6")}, rate.NewShaper())
	if !slices.EqualFunc(b.dns, []net.IP{net.ParseIP("2606:4700:4700::1111"), net.ParseIP("2001:4860:4860::8888")}, net.IP.Equal) {
		t.Errorf("asks %v", b.dns)
	}
	if !bytes.Contains(logged.Bytes(), []byte("Phone has only IPv6 and no DNS server")) {
		t.Errorf("logged %q", logged.String())
	}
	if b := NewBinding(Interface{Name: "Eth", IPv4: net.IPv4(192, 168, 1, 2), IPv6: net.ParseIP("2001:470:1::6")}, rate.NewShaper()); !slices.EqualFunc(b.dns, fallbackDNS4, net.IP.Equal) {
		t.Errorf("with IPv4 the fallback stays 1.1.1.1 and 8.8.8.8, got %v", b.dns)
	}
}

// dualServer serves on 127.0.0.1 and, unless v6 is false, on ::1 at the same
// port, answering each request with the family it came over.
func dualServer(t *testing.T, v6 bool) string {
	t.Helper()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if net.ParseIP(host).To4() != nil {
			w.Write([]byte("IPv4"))
		} else {
			w.Write([]byte("IPv6"))
		}
	})}
	ln4, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln4)
	port := strconv.Itoa(ln4.Addr().(*net.TCPAddr).Port)
	if v6 {
		ln6, err := net.Listen("tcp6", net.JoinHostPort("::1", port))
		if err != nil {
			t.Skip("no IPv6 loopback on the same port: ", err)
		}
		go srv.Serve(ln6)
	}
	t.Cleanup(func() { srv.Close() })
	return port
}

func get(t *testing.T, c *http.Client, url string) string {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func dualLoopback(t *testing.T) Interface {
	lo := loopback6(t)
	lo.IPv4, lo.DNS = net.IPv4(127, 0, 0, 1), []net.IP{net.IPv6loopback}
	return lo
}

// A dual-stack interface reaches a name over IPv6 when it can (Happy
// Eyeballs), over IPv4 when IPv6 fails, and its family clients keep to their
// family.
func TestDualStackDialsIPv6FirstAndFallsBackToIPv4(t *testing.T) {
	dnsServer(t, net.IPv4(127, 0, 0, 1), net.IPv6loopback)
	b := NewBinding(dualLoopback(t), rate.NewShaper())
	defer b.Close()
	url := "http://dual.test:" + dualServer(t, true) + "/"
	if got := get(t, b.HTTPClient(), url); got != "IPv6" {
		t.Errorf("came over %s, want IPv6 first", got)
	}
	for _, f := range []string{"IPv4", "IPv6"} {
		if got := get(t, b.FamilyClients()[f], url); got != f {
			t.Errorf("the %s client came over %s", f, got)
		}
	}

	// Nothing listens on ::1 at this port: IPv6 is refused, IPv4 takes over at once.
	url = "http://dual.test:" + dualServer(t, false) + "/"
	start := time.Now()
	if got := get(t, b.HTTPClient(), url); got != "IPv4" {
		t.Errorf("came over %s, want IPv4 after IPv6 failed", got)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("falling back took %v", took)
	}
	if NewBinding(loopback(t), rate.NewShaper()).FamilyClients() != nil {
		t.Error("an IPv4-only interface has family clients")
	}
}
