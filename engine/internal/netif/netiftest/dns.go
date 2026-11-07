// Package netiftest has a DNS server on loopback for tests of bindings that
// resolve names: point netif.DNSPort at its port.
package netiftest

import (
	"net"
	"slices"
	"strconv"
	"sync"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// DNS answers every name with a (A) and aaaa (AAAA), either may be nil for no
// answer, on 127.0.0.1 and ::1 at one port. asked returns the query types
// received since it was last called.
func DNS(t *testing.T, a, aaaa net.IP) (port string, asked func() []dnsmessage.Type) {
	t.Helper()
	var mu sync.Mutex
	var types []dnsmessage.Type
	pc4, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := pc4.LocalAddr().(*net.UDPAddr).Port
	pc6, err := net.ListenPacket("udp6", net.JoinHostPort("::1", strconv.Itoa(p)))
	if err != nil {
		pc4.Close()
		t.Skip("no IPv6 loopback on the same port: ", err)
	}
	t.Cleanup(func() { pc4.Close(); pc6.Close() })
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
			types = append(types, q.Type)
			mu.Unlock()
			m.Header.Response, m.Header.RecursionAvailable = true, true
			h := dnsmessage.ResourceHeader{Name: q.Name, Type: q.Type, Class: q.Class, TTL: 60}
			switch {
			case q.Type == dnsmessage.TypeA && a != nil:
				m.Answers = []dnsmessage.Resource{{Header: h, Body: &dnsmessage.AResource{A: [4]byte(a.To4())}}}
			case q.Type == dnsmessage.TypeAAAA && aaaa != nil:
				m.Answers = []dnsmessage.Resource{{Header: h, Body: &dnsmessage.AAAAResource{AAAA: [16]byte(aaaa.To16())}}}
			}
			if out, err := m.Pack(); err == nil {
				pc.WriteTo(out, from)
			}
		}
	}
	go serve(pc4)
	go serve(pc6)
	return strconv.Itoa(p), func() []dnsmessage.Type {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(types)
		types = nil
		return out
	}
}
