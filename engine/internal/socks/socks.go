// Package socks is a minimal local SOCKS5 server (CONNECT, no auth). Firefox
// routes a container's traffic through it so pages load via one interface.
package socks

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"time"
)

type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

type Server struct {
	ln   net.Listener
	dial DialFunc
}

// Listen starts a server on a random loopback port.
func Listen(dial DialFunc) (*Server, error) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{ln: ln, dial: dial}
	go s.serve()
	return s, nil
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
	repHostUnreachable  = 4
	repCmdNotSupported  = 7
	repAddrNotSupported = 8
)

func (s *Server) handle(c net.Conn) {
	defer c.Close()
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
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(p[:]))))

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	up, err := s.dial(ctx, "tcp", target)
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

func reply(c net.Conn, code byte) error {
	_, err := c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
	return err
}

func closeWrite(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.CloseWrite()
	}
}
