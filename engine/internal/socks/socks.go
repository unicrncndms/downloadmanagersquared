// Package socks is a minimal local SOCKS5 server (CONNECT, no auth). Firefox
// routes a container's traffic through it so pages load via one interface.
//
// It takes connections only from processes of the engine's own user, and
// connects only to public addresses, so it can't be used to reach the local
// network or the PC itself. IPv6 targets only through a path with IPv6.
package socks

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"downloadmanagersquared/engine/internal/netif"
	"downloadmanagersquared/engine/internal/winutil"
)

type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Path is where the server's connections go out: an interface or a tunnel,
// with its own name resolution.
type Path struct {
	Dial   DialFunc
	Lookup func(ctx context.Context, host string) ([]net.IP, error) // A, and AAAA where the path has IPv6
	// IPv6 is whether the path has IPv6 now (Enable IPv6 on, and an IPv6
	// address); nil: never. Without it IPv6 targets are refused as unsupported.
	IPv6 func() bool
}

// Route is where a path's connections go out at one moment: its dialer and its name lookup.
type Route struct {
	Dial   DialFunc
	Lookup func(ctx context.Context, host string) ([]net.IP, error)
}

// Via is a Path that takes its route at each use, so a path that goes down,
// or comes back, is seen at once: route fails while the path is down.
func Via(route func() (Route, error), ipv6 func() bool) Path {
	return Path{
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			r, err := route()
			if err != nil {
				return nil, err
			}
			return r.Dial(ctx, network, addr)
		},
		Lookup: func(ctx context.Context, host string) ([]net.IP, error) {
			r, err := route()
			if err != nil {
				return nil, err
			}
			return r.Lookup(ctx, host)
		},
		IPv6: ipv6,
	}
}

type Server struct {
	ln   net.Listener
	path Path
	sid  string // the engine's user: the only one let in

	mu      sync.Mutex
	refused map[uint32]bool   // processes already logged as refused
	relays  map[net.Conn]bool // connections of relays under way, so Close ends them
	closed  bool
}

// Listen starts a server on a random loopback port.
func Listen(p Path) (*Server, error) {
	sid, err := winutil.CurrentUserSID()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{ln: ln, path: p, sid: sid, refused: map[uint32]bool{}, relays: map[net.Conn]bool{}}
	go s.serve()
	return s, nil
}

// clientOwner is the process, and its user, at the client end of a loopback
// connection to the server.
var clientOwner = func(client, server *net.TCPAddr) (uint32, string, error) {
	pid, err := winutil.TCPOwnerPID(client, server)
	if err != nil {
		return 0, "", err
	}
	user, err := winutil.ProcessUser(pid)
	return pid, user, err
}

// fromOwnUser is whether c comes from a process of the engine's user. Anything
// it can't tell fails closed.
func (s *Server) fromOwnUser(c net.Conn) bool {
	client, _ := c.RemoteAddr().(*net.TCPAddr)
	server, _ := c.LocalAddr().(*net.TCPAddr)
	pid, user, err := clientOwner(client, server)
	if err == nil && user == s.sid {
		return true
	}
	s.mu.Lock()
	first := !s.refused[pid]
	s.refused[pid] = true
	s.mu.Unlock()
	if first {
		if err == nil {
			err = errors.New("another user's process")
		}
		log.Printf("socks %d: refused process %d: %v", s.Port(), pid, err)
	}
	return false
}

func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// Close stops listening and ends the relays under way.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	for c := range s.relays {
		c.Close()
	}
	s.mu.Unlock()
	return s.ln.Close()
}

// track adds a relay's connections to the ones Close ends; false once the server is closed.
func (s *Server) track(conns ...net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	for _, c := range conns {
		s.relays[c] = true
	}
	return true
}

func (s *Server) untrack(conns ...net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range conns {
		delete(s.relays, c)
	}
}

func (s *Server) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

const (
	repSucceeded        = 0
	repGeneralFailure   = 1
	repNotAllowed       = 2
	repHostUnreachable  = 4
	repCmdNotSupported  = 7
	repAddrNotSupported = 8
)

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	if !s.fromOwnUser(c) {
		return
	}
	c.SetDeadline(time.Now().Add(30 * time.Second))

	var hdr [2]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil || hdr[0] != 5 {
		return
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}

	v6 := s.path.IPv6 != nil && s.path.IPv6()
	host, port, asked6, ok := s.readRequest(c, v6)
	if !ok {
		return
	}
	// Replies carry the family asked for, then that of the address dialled.
	six := asked6
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	ips, err := s.publicAddrs(ctx, host, v6)
	if err != nil {
		replyFor(c, repHostUnreachable, six)
		return
	}
	if len(ips) == 0 {
		replyFor(c, repNotAllowed, six)
		return
	}
	var up net.Conn
	up, six, err = s.dialFirst(ctx, ips, port)
	cancel()
	if err != nil {
		var ne net.Error
		code := byte(repGeneralFailure)
		if errors.As(err, &ne) {
			code = repHostUnreachable
		}
		replyFor(c, code, six)
		return
	}
	defer up.Close()
	if !s.track(c, up) {
		return
	}
	defer s.untrack(c, up)
	if replyFor(c, repSucceeded, six) != nil {
		return
	}
	c.SetDeadline(time.Time{})

	done := make(chan struct{}, 2)
	go func() { io.Copy(up, c); closeWrite(up); done <- struct{}{} }()
	go func() { io.Copy(c, up); closeWrite(c); done <- struct{}{} }()
	<-done
	<-done
}

// readRequest reads a CONNECT request: the target host and port, and whether
// the client gave an IPv6 address. It answers the requests it refuses itself;
// ok is false when the connection is to be dropped.
func (s *Server) readRequest(c net.Conn, v6 bool) (host, port string, asked6, ok bool) {
	var req [4]byte
	if _, err := io.ReadFull(c, req[:]); err != nil || req[0] != 5 {
		return "", "", false, false
	}
	if req[1] != 1 {
		reply(c, repCmdNotSupported)
		return "", "", false, false
	}
	switch req[3] {
	case 4:
		var ip [16]byte
		if _, err := io.ReadFull(c, ip[:]); err != nil {
			return "", "", false, false
		}
		if !v6 {
			// The port read too: closing with it unread, Windows resets the
			// connection and the reply can be lost.
			io.ReadFull(c, ip[:2])
			reply(c, repAddrNotSupported)
			return "", "", false, false
		}
		host = net.IP(ip[:]).String()
	case 1:
		var ip [4]byte
		if _, err := io.ReadFull(c, ip[:]); err != nil {
			return "", "", false, false
		}
		host = net.IP(ip[:]).String()
	case 3:
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return "", "", false, false
		}
		name := make([]byte, l[0])
		if _, err := io.ReadFull(c, name); err != nil {
			return "", "", false, false
		}
		host = string(name)
	default:
		reply(c, repAddrNotSupported)
		return "", "", false, false
	}
	var p [2]byte
	if _, err := io.ReadFull(c, p[:]); err != nil {
		return "", "", false, false
	}
	port = strconv.Itoa(int(binary.BigEndian.Uint16(p[:])))
	return host, port, req[3] == 4, true
}

// dialFirst dials the first of ips that connects, and says whether the address
// it connected to is IPv6.
func (s *Server) dialFirst(ctx context.Context, ips []net.IP, port string) (up net.Conn, six bool, err error) {
	for _, ip := range ips { // the address checked is the one dialled: no second lookup to rebind
		six = ip.To4() == nil
		if up, err = s.path.Dial(ctx, "tcp", net.JoinHostPort(ip.String(), port)); err == nil {
			break
		}
	}
	return up, six, err
}

// publicAddrs resolves host through the path and keeps its public addresses:
// IPv4 ones, and IPv6 ones when v6.
func (s *Server) publicAddrs(ctx context.Context, host string, v6 bool) ([]net.IP, error) {
	ips := []net.IP{net.ParseIP(host)}
	if ips[0] == nil {
		var err error
		if ips, err = s.path.Lookup(ctx, host); err != nil {
			return nil, err
		}
	}
	var out []net.IP
	for _, ip := range ips {
		if public(ip, v6) {
			out = append(out, ip)
		}
	}
	return out, nil
}

// IPv6 targets never let through: loopback, unspecified, mapped or compatible
// with an IPv4 address, link-local, site-local, unique local, multicast and
// documentation.
var refused6 = []*net.IPNet{cidr("::1/128"), cidr("::/128"), cidr("::/96"), cidr("fe80::/10"), cidr("fec0::/10"),
	cidr("fc00::/7"), cidr("ff00::/8"), cidr("2001:db8::/32")}

// nat64 is the well-known NAT64 prefix: the IPv4 address in its last 32 bits
// is what is reached.
var nat64 = cidr("64:ff9b::/96")

// sixToFour is the 6to4 prefix: the IPv4 address in bits 16 to 48 is what is reached.
var sixToFour = cidr("2002::/16")

// teredo is the Teredo prefix: the client's IPv4 address is in the last 32
// bits, inverted.
var teredo = cidr("2001::/32")

func cidr(s string) *net.IPNet {
	_, n, _ := net.ParseCIDR(s)
	return n
}

// public is whether ip may be dialled: a public IPv4 address, or with v6 a
// public IPv6 one. An IPv4-mapped (::ffff:0:0/96), NAT64, 6to4 or Teredo
// address counts as the IPv4 address inside it.
func public(ip net.IP, v6 bool) bool {
	if ip.To4() != nil { // also an IPv4-mapped address
		return netif.PublicIPv4(ip)
	}
	if !v6 || len(ip) != net.IPv6len {
		return false
	}
	switch {
	case nat64.Contains(ip):
		return netif.PublicIPv4(ip[12:])
	case sixToFour.Contains(ip):
		return netif.PublicIPv4(ip[2:6])
	case teredo.Contains(ip):
		return netif.PublicIPv4(net.IP{^ip[12], ^ip[13], ^ip[14], ^ip[15]})
	}
	for _, n := range refused6 {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

func reply(c net.Conn, code byte) error { return replyFor(c, code, false) }

// replyFor answers with code and an empty bound address of the target's family.
func replyFor(c net.Conn, code byte, v6 bool) error {
	msg := []byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0}
	if v6 {
		msg = append([]byte{5, code, 0, 4}, make([]byte, 18)...)
	}
	_, err := c.Write(msg)
	return err
}

func closeWrite(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.CloseWrite()
	}
}
