package paths

import (
	"context"
	"net"
	"net/http"
	"testing"
)

type fakeBinding struct{ six bool }

func (fakeBinding) DialContext(context.Context, string, string) (net.Conn, error) { return nil, nil }
func (fakeBinding) LookupIP(context.Context, string) ([]net.IP, error)            { return nil, nil }
func (fakeBinding) ListenPacket(int) (net.PacketConn, error)                      { return nil, nil }
func (fakeBinding) Listen(int) (net.Listener, error)                              { return nil, nil }
func (fakeBinding) ListenPacket6(int) (net.PacketConn, error)                     { return nil, nil }
func (fakeBinding) Listen6(int) (net.Listener, error)                             { return nil, nil }
func (fakeBinding) HTTPClient() *http.Client                                      { return &http.Client{} }
func (fakeBinding) FamilyClients() map[string]*http.Client                        { return nil }
func (b fakeBinding) Has6() bool                                                  { return b.six }

type fakeSource struct {
	facts  map[string]Facts
	tunnel map[string]Stack // present = up
	gone   bool             // the bare binding has vanished since the facts
}

func (s *fakeSource) Binding(id string) (Binding, error) {
	if s.gone {
		return nil, errNoIface
	}
	return s.facts[id].Binding, nil
}
func (s *fakeSource) Tunnel(id string) (Stack, bool) { t, ok := s.tunnel[id]; return t, ok }
func (s *fakeSource) Iface(id string) Facts          { return s.facts[id] }

func snap(up bool) Snapshot {
	return Snapshot{Ifaces: []Iface{{ID: "a", Present: true, Enabled: true, TunnelUp: up}}}
}

func TestTunnelCarriesOnlyIPv4(t *testing.T) {
	src := &fakeSource{
		facts:  map[string]Facts{"a": {Binding: fakeBinding{six: true}}},
		tunnel: map[string]Stack{"a": {Client: &http.Client{}}},
	}
	ps := Build(Alongside, snap(true), src)
	if len(ps) != 2 {
		t.Fatalf("got %d paths, want the interface and its tunnel", len(ps))
	}
	bare, tun := ps[0], ps[1]
	if bare.ID != "a" || bare.Listen6 == nil || bare.ListenPacket6 == nil || !bare.IPv6() {
		t.Errorf("a bare interface with IPv6 should listen on it: %+v", bare)
	}
	if tun.ID != Tunnel("a") || tun.Listen6 != nil || tun.ListenPacket6 != nil || tun.IPv6() || tun.ByFamily != nil {
		t.Errorf("a tunnel must carry only IPv4: %+v", tun)
	}
}

func TestTunnelSharesItsInterfacesSettings(t *testing.T) {
	src := &fakeSource{
		facts:  map[string]Facts{"a": {Conns: 7, Upload: false, Gen: 3, Binding: fakeBinding{}}},
		tunnel: map[string]Stack{"a": {Gen: 9, Port: 4100}},
	}
	tun := Build(VPNOnly, snap(true), src)[0]
	if tun.Conns != 7 || tun.Upload {
		t.Errorf("tunnel conns %d, upload %v; want its interface's 7 and false", tun.Conns, tun.Upload)
	}
	if tun.Gen != 9 || tun.Port != 4100 {
		t.Errorf("tunnel gen %d, port %d; want its own 9 and 4100", tun.Gen, tun.Port)
	}
}

func TestTunnelThatWentDownIsSkipped(t *testing.T) {
	src := &fakeSource{facts: map[string]Facts{"a": {Binding: fakeBinding{}}}} // the snapshot said up, it is not
	if ps := Build(VPNOnly, snap(true), src); len(ps) != 0 {
		t.Errorf("got %d paths, want none", len(ps))
	}
	if _, err := Open(src, Tunnel("a")); err == nil || err.Error() != "the VPN tunnel is not up" {
		t.Errorf("Open error %v", err)
	}
	if ps := Build(Alongside, snap(true), src); len(ps) != 1 || ps[0].ID != "a" {
		t.Errorf("the bare interface should remain: %+v", ps)
	}
}

func TestBareLooksUpItsBindingOnEachCall(t *testing.T) {
	src := &fakeSource{facts: map[string]Facts{"a": {Binding: fakeBinding{}}}}
	p := Build(Off, snap(false), src)[0]
	if _, err := p.Listen(0); err != nil {
		t.Fatal(err)
	}
	src.gone = true
	if _, err := p.Listen(0); err == nil || err.Error() != "interface is not connected" {
		t.Errorf("Listen error %v", err)
	}
	if p.IPv6() {
		t.Error("IPv6 after the interface is gone")
	}
}
