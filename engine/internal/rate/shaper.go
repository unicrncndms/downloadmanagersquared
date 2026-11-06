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
// limits together, and to its parent's (Under) as well: the lower one wins.
type Shaper struct {
	up, down *xrate.Limiter
	parent   *Shaper
}

// Under makes p s's parent: everything s shapes counts against p's limits too.
func (s *Shaper) Under(p *Shaper) *Shaper {
	s.parent = p
	return s
}

// Took waits until n bytes received fit the download limits, or ctx ends.
func (s *Shaper) Took(ctx context.Context, n int) error {
	for ; n > 0; n -= shaperBurst {
		for t := s; t != nil; t = t.parent {
			if err := t.down.WaitN(ctx, min(n, shaperBurst)); err != nil {
				return err
			}
		}
	}
	return nil
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

func (s *Shaper) waitUp(n int) {
	for t := s; t != nil && n > 0; t = t.parent {
		t.up.WaitN(context.Background(), min(n, shaperBurst))
	}
}

func (s *Shaper) waitDown(n int) {
	for t := s; t != nil && n > 0; t = t.parent {
		t.down.WaitN(context.Background(), min(n, shaperBurst))
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
	c.s.waitDown(n)
	return n, err
}

func (c *shapedConn) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		chunk := p[:min(len(p), shaperBurst)]
		c.s.waitUp(len(chunk))
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
	c.s.waitDown(n)
	return n, addr, err
}

func (c *shapedPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	c.s.waitUp(len(p))
	return c.PacketConn.WriteTo(p, addr)
}
