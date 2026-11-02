package rate

import (
	"context"
	"net"

	xrate "golang.org/x/time/rate"
)

// shaperBurst is the most a connection moves in one go; reads and writes are
// cut to it.
const shaperBurst = 32 << 10

// Shaper holds every connection on one interface to its upload and download
// limits together.
type Shaper struct {
	up, down *xrate.Limiter
}

func NewShaper() *Shaper {
	return &Shaper{
		up:   xrate.NewLimiter(xrate.Inf, shaperBurst),
		down: xrate.NewLimiter(xrate.Inf, shaperBurst),
	}
}

// SetLimits sets the limits in bytes per second; 0 is none.
func (s *Shaper) SetLimits(up, down int64) {
	set := func(l *xrate.Limiter, bps int64) {
		if bps <= 0 {
			l.SetLimit(xrate.Inf)
		} else {
			l.SetLimit(xrate.Limit(bps))
		}
	}
	set(s.up, up)
	set(s.down, down)
}

// Limits are the limits in bytes per second; 0 is none.
func (s *Shaper) Limits() (up, down int64) {
	get := func(l *xrate.Limiter) int64 {
		if l.Limit() == xrate.Inf {
			return 0
		}
		return int64(l.Limit())
	}
	return get(s.up), get(s.down)
}

// Conn counts c against the limits.
func (s *Shaper) Conn(c net.Conn) net.Conn { return &shapedConn{Conn: c, s: s} }

// PacketConn counts pc's datagrams against the limits.
func (s *Shaper) PacketConn(pc net.PacketConn) net.PacketConn {
	return &shapedPacketConn{PacketConn: pc, s: s}
}

func wait(l *xrate.Limiter, n int) {
	if n > 0 {
		l.WaitN(context.Background(), min(n, shaperBurst))
	}
}

type shapedConn struct {
	net.Conn
	s *Shaper
}

func (c *shapedConn) Read(p []byte) (int, error) {
	if len(p) > shaperBurst {
		p = p[:shaperBurst]
	}
	n, err := c.Conn.Read(p)
	wait(c.s.down, n)
	return n, err
}

func (c *shapedConn) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		chunk := p[:min(len(p), shaperBurst)]
		wait(c.s.up, len(chunk))
		n, err := c.Conn.Write(chunk)
		written += n
		if err != nil {
			return written, err
		}
		p = p[len(chunk):]
	}
	return written, nil
}

type shapedPacketConn struct {
	net.PacketConn
	s *Shaper
}

func (c *shapedPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(p)
	wait(c.s.down, n)
	return n, addr, err
}

func (c *shapedPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	wait(c.s.up, len(p))
	return c.PacketConn.WriteTo(p, addr)
}
