package paths

import (
	"context"
	"errors"
	"net"
	"net/http"
)

// Binding is a bare interface's sockets, as *netif.Binding offers them.
type Binding interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
	LookupIP(ctx context.Context, host string) ([]net.IP, error)
	ListenPacket(port int) (net.PacketConn, error)
	Listen(port int) (net.Listener, error)
	ListenPacket6(port int) (net.PacketConn, error)
	Listen6(port int) (net.Listener, error)
	HTTPClient() *http.Client
	FamilyClients() map[string]*http.Client
	Has6() bool
}

// Stack is a tunnel that was up when Source.Tunnel returned it. A tunnel
// carries only IPv4, so there is nothing for IPv6 here.
type Stack struct {
	Gen          int // its start: sockets of an older one are on a dead stack
	Port         int // where torrent peers come in; 0 = any
	Client       *http.Client
	Dial         func(ctx context.Context, network, addr string) (net.Conn, error)
	LookupIP     func(ctx context.Context, host string) ([]net.IP, error)
	Listen       func(port int) (net.Listener, error)
	ListenPacket func(port int) (net.PacketConn, error)
}

// Facts are what the engine knows about one interface, tunnel or not.
type Facts struct {
	Gen     int     // goes up with each new binding
	Conns   int     // connections per download
	Upload  bool    // peers may download from us through it
	Binding Binding // its current binding; nil when it is gone
}

// Source is the engine as Open reaches interfaces and tunnels. Iface is
// called under the engine's lock, so it must not call Binding.
type Source interface {
	Binding(ifaceID string) (Binding, error) // the current binding, looked up on each call
	Tunnel(ifaceID string) (Stack, bool)
	Iface(ifaceID string) Facts
}

// Path is a usable path, ready to use: the sockets, the HTTP clients and the
// settings that apply to it, whether it is an interface or its tunnel.
type Path struct {
	ID       string
	Gen      int
	Port     int // forwarded port of a tunnel; 0 otherwise
	Upload   bool
	Conns    int
	Client   *http.Client
	ByFamily map[string]*http.Client // nil without IPv6, and for a tunnel

	Dial         func(ctx context.Context, network, addr string) (net.Conn, error)
	LookupIP     func(ctx context.Context, host string) ([]net.IP, error)
	ListenPacket func(port int) (net.PacketConn, error)
	Listen       func(port int) (net.Listener, error)
	// Over IPv6: nil unless the path is an interface with IPv6 on.
	ListenPacket6 func(port int) (net.PacketConn, error)
	Listen6       func(port int) (net.Listener, error)
	// IPv6 is whether the path has IPv6 now; always false for a tunnel.
	IPv6 func() bool
}

var (
	errTunnelDown = errors.New("the VPN tunnel is not up")
	errNoIface    = errors.New("interface is not connected")
)

// Build is the paths a download in mode m may use, the main first. A tunnel
// that went down since the snapshot is skipped.
func Build(m Mode, s Snapshot, src Source) []Path {
	var out []Path
	for _, id := range For(m, s).Paths {
		if p, err := Open(src, id); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// Open is one path by its ID. A tunnel's functions are bound to its stack as
// it is now (a restart is a new Gen); an interface's look up its binding on
// each call, so a new address or a lost interface shows at once.
func Open(src Source, id string) (Path, error) {
	base, tun := IfaceOf(id)
	f := src.Iface(base)
	p := Path{ID: id, Gen: f.Gen, Upload: f.Upload, Conns: f.Conns}
	if tun {
		tn, ok := src.Tunnel(base)
		if !ok {
			return Path{}, errTunnelDown
		}
		p.Gen, p.Port, p.Client = tn.Gen, tn.Port, tn.Client
		p.Dial, p.LookupIP, p.ListenPacket, p.Listen = tn.Dial, tn.LookupIP, tn.ListenPacket, tn.Listen
		p.IPv6 = func() bool { return false }
		return p, nil
	}
	if f.Binding == nil {
		return Path{}, errNoIface
	}
	// b is the interface's binding as it is at the time of the call.
	b := func() (Binding, error) { return src.Binding(base) }
	p.Client, p.ByFamily = f.Binding.HTTPClient(), f.Binding.FamilyClients()
	p.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		cur, err := b()
		if err != nil {
			return nil, err
		}
		return cur.DialContext(ctx, network, addr)
	}
	p.LookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
		cur, err := b()
		if err != nil {
			return nil, err
		}
		return cur.LookupIP(ctx, host)
	}
	p.ListenPacket = func(port int) (net.PacketConn, error) {
		cur, err := b()
		if err != nil {
			return nil, err
		}
		return cur.ListenPacket(port)
	}
	p.Listen = func(port int) (net.Listener, error) {
		cur, err := b()
		if err != nil {
			return nil, err
		}
		return cur.Listen(port)
	}
	p.IPv6 = func() bool {
		cur, err := b()
		return err == nil && cur.Has6()
	}
	if f.Binding.Has6() { // only with Enable IPv6 on
		p.ListenPacket6 = func(port int) (net.PacketConn, error) {
			cur, err := b()
			if err != nil {
				return nil, err
			}
			return cur.ListenPacket6(port)
		}
		p.Listen6 = func(port int) (net.Listener, error) {
			cur, err := b()
			if err != nil {
				return nil, err
			}
			return cur.Listen6(port)
		}
	}
	return p, nil
}
