// Package socks is a minimal local SOCKS5 server (CONNECT, no auth). Firefox
// routes a container's traffic through it so pages load via one interface.
//
// It takes connections only from processes of the engine's own user, and
// connects only to public IPv4 addresses, so it can't be used to reach the
// local network or the PC itself.
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
	Lookup func(ctx context.Context, host string) ([]net.IP, error)
}

type Server struct {
	ln   net.Listener
	path Path
	sid  string // the engine's user: the only one let in

	mu      sync.Mutex
	refused map[uint32]bool // processes already logged as refused
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
	s := &Server{ln: ln, path: p, sid: sid, refused: map[uint32]bool{}}
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

func (s *Server) Port() int    { return s.ln.Addr().(*net.TCPAddr).Port }
func (s *Server) Close() error { return s.ln.Close() }

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

	var req [4]byte
	if _, err := io.ReadFull(c, req[:]); err != nil || req[0] != 5 {
		return
	}
	if req[1] != 1 {
		reply(c, repCmdNotSupported)
		return
	}
	var host string
	switch req[3] {
	case 1:
		var ip [4]byte
		if _, err := io.ReadFull(c, ip[:]); err != nil {
			return
		}
		host = net.IP(ip[:]).String()
	case 3:
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return
		}
		name := make([]byte, l[0])
		if _, err := io.ReadFull(c, name); err != nil {
			return
		}
		host = string(name)
	default:
		reply(c, repAddrNotSupported)
		return
	}
	var p [2]byte
	if _, err := io.ReadFull(c, p[:]); err != nil {
		return
	}
	port := strconv.Itoa(int(binary.BigEndian.Uint16(p[:])))

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	ips, err := s.publicAddrs(ctx, host)
	if err != nil {
		reply(c, repHostUnreachable)
		return
	}
	if len(ips) == 0 {
		reply(c, repNotAllowed)
		return
	}
	var up net.Conn
	for _, ip := range ips { // the address checked is the one dialled: no second lookup to rebind
		if up, err = s.path.Dial(ctx, "tcp", net.JoinHostPort(ip.String(), port)); err == nil {
			break
		}
	}
	cancel()
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) {
			reply(c, repHostUnreachable)
		} else {
			reply(c, repGeneralFailure)
		}
		return
	}
	defer up.Close()
	if reply(c, repSucceeded) != nil {
		return
	}
	c.SetDeadline(time.Time{})

	done := make(chan struct{}, 2)
	go func() { io.Copy(up, c); closeWrite(up); done <- struct{}{} }()
	go func() { io.Copy(c, up); closeWrite(c); done <- struct{}{} }()
	<-done
	<-done
}

// publicAddrs resolves host through the path and keeps its public IPv4 addresses.
func (s *Server) publicAddrs(ctx context.Context, host string) ([]net.IP, error) {
	ips := []net.IP{net.ParseIP(host)}
	if ips[0] == nil {
		var err error
		if ips, err = s.path.Lookup(ctx, host); err != nil {
			return nil, err
		}
	}
	var out []net.IP
	for _, ip := range ips {
		if netif.PublicIPv4(ip) {
			out = append(out, ip)
		}
	}
	return out, nil
}

func reply(c net.Conn, code byte) error {
	_, err := c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
	return err
}

func closeWrite(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.CloseWrite()
	}
}
