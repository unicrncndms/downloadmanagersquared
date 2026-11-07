package netif

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

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

// dnsServer answers A with 192.0.2.1 and AAAA with 2001:db8::1 on 127.0.0.1
// and ::1, recording the query types it is asked, and points dnsPort at itself.
func dnsServer(t *testing.T) func() []dnsmessage.Type {
	t.Helper()
	var mu sync.Mutex
	var asked []dnsmessage.Type
	pc4, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := pc4.LocalAddr().(*net.UDPAddr).Port
	pc6, err := net.ListenPacket("udp6", net.JoinHostPort("::1", strconv.Itoa(port)))
	if err != nil {
		pc4.Close()
		t.Skip("no IPv6 loopback on the same port: ", err)
	}
	old := dnsPort
	dnsPort = strconv.Itoa(port)
	t.Cleanup(func() { dnsPort = old; pc4.Close(); pc6.Close() })
	serve := func(pc net.PacketConn) {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var m dnsmessage.Message
			if m.Unpack(buf[:n]) != nil || len(m.Questions) != 1 {
				continue
			}
			q := m.Questions[0]
			mu.Lock()
			asked = append(asked, q.Type)
			mu.Unlock()
			m.Header.Response, m.Header.RecursionAvailable = true, true
			h := dnsmessage.ResourceHeader{Name: q.Name, Type: q.Type, Class: q.Class, TTL: 60}
			switch q.Type {
			case dnsmessage.TypeA:
				m.Answers = []dnsmessage.Resource{{Header: h, Body: &dnsmessage.AResource{A: [4]byte{192, 0, 2, 1}}}}
			case dnsmessage.TypeAAAA:
				m.Answers = []dnsmessage.Resource{{Header: h, Body: &dnsmessage.AAAAResource{AAAA: [16]byte(net.ParseIP("2001:db8::1"))}}}
			}
			if out, err := m.Pack(); err == nil {
				pc.WriteTo(out, from)
			}
		}
	}
	go serve(pc4)
	go serve(pc6)
	return func() []dnsmessage.Type {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(asked)
		asked = nil
		return out
	}
}

// Each interface asks only for the families it has, and over the families it
// has: an IPv6 DNS server through its IPv6 address.
func TestLookupAsksOnlyForTheInterfacesFamilies(t *testing.T) {
	asked := dnsServer(t)
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
