package torrent

import (
	"net"
	"testing"
)

// deadConn and deadListener answer nil for their addresses, as a tunnel's do
// once its stack is closed.
type deadConn struct{ net.Conn }

func (deadConn) LocalAddr() net.Addr  { return nil }
func (deadConn) RemoteAddr() net.Addr { return nil }
func (deadConn) Close() error         { return nil }

type deadListener struct{ conn net.Conn }

func (l deadListener) Accept() (net.Conn, error) { return l.conn, nil }
func (deadListener) Addr() net.Addr              { return nil }

// The torrent client calls Network and String on these addresses without
// checking: none may be nil, even from a tunnel that has just closed.
func TestAClosedTunnelsAddressesNeverReachTheClientAsNil(t *testing.T) {
	s, err := NewSession(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	is := &ifaceSockets{Iface: Iface{ID: "lo~vpn"}, open: map[*trackedPeer]bool{}, gone: make(chan struct{})}

	l := &peerListener{s: s, is: is, l: deadListener{deadConn{}}, addr: addrOr(deadListener{}.Addr(), "tcp", ""), done: make(chan struct{})}
	if a := l.Addr(); a == nil || a.Network() != "tcp" {
		t.Fatalf("listener address %v", a)
	}
	if c, err := l.Accept(); err == nil || c != nil {
		t.Fatalf("a peer whose tunnel closed as it came in was accepted: %v, %v", c, err)
	}

	s.mu.Lock()
	p := s.trackLocked("203.0.113.5:6881", is, deadConn{})
	s.mu.Unlock()
	if p.LocalAddr() == nil || p.RemoteAddr() == nil || p.RemoteAddr().String() != "203.0.113.5:6881" {
		t.Fatalf("peer addresses %v, %v", p.LocalAddr(), p.RemoteAddr())
	}
}
