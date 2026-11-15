package rate

import (
	"context"
	"net"
	"sync/atomic"

	xrate "golang.org/x/time/rate"
)

// shaperBurst is the most a connection moves in one go; reads and writes are
// cut to it.
const shaperBurst = 32 << 10

// Shaper holds every connection on one interface to its upload and download
// limits together, and to its parent's (Under) as well: the lower one wins.
type Shaper struct {
	up, down lane
	parent   *Shaper
}

// lane is one direction's limiter. on mirrors whether it holds a limit, so the
// hot path reads one atomic instead of the limiter's mutex.
type lane struct {
	lim *xrate.Limiter
	on  atomic.Bool
}

// Under makes p s's parent: everything s shapes counts against p's limits too.
func (s *Shaper) Under(p *Shaper) *Shaper {
	s.parent = p
	return s
}

// Took waits until n bytes received fit the download limits, or ctx ends.
func (s *Shaper) Took(ctx context.Context, n int) error {
	return s.wait(ctx, pickDown, n)
}

func pickUp(t *Shaper) *lane   { return &t.up }
func pickDown(t *Shaper) *lane { return &t.down }

// wait takes n bytes from the limiters pick finds in s and its parents, or
// returns the error once ctx ends. Nothing limited means no wait at all.
func (s *Shaper) wait(ctx context.Context, pick func(*Shaper) *lane, n int) error {
	if !s.limited(pick) {
		return nil
	}
	for ; n > 0; n -= shaperBurst {
		for t := s; t != nil; t = t.parent {
			if err := pick(t).lim.WaitN(ctx, min(n, shaperBurst)); err != nil {
				return err
			}
		}
	}
	return nil
}

// limited is whether s or a parent has a limit in the direction pick finds.
func (s *Shaper) limited(pick func(*Shaper) *lane) bool {
	for t := s; t != nil; t = t.parent {
		if pick(t).on.Load() {
			return true
		}
	}
	return false
}

func NewShaper() *Shaper {
	return &Shaper{
		up:   lane{lim: xrate.NewLimiter(xrate.Inf, shaperBurst)},
		down: lane{lim: xrate.NewLimiter(xrate.Inf, shaperBurst)},
	}
}

// SetLimits sets the limits in bytes per second; 0 is none.
func (s *Shaper) SetLimits(up, down int64) {
	set := func(l *lane, bps int64) {
		if bps <= 0 {
			l.lim.SetLimit(xrate.Inf)
		} else {
			l.lim.SetLimit(xrate.Limit(bps))
		}
		l.on.Store(bps > 0) // after the limiter, so a waiter that sees it finds the limit
	}
	set(&s.up, up)
	set(&s.down, down)
}

// Limits are the limits in bytes per second; 0 is none.
func (s *Shaper) Limits() (up, down int64) {
	get := func(l *lane) int64 {
		if l.lim.Limit() == xrate.Inf {
			return 0
		}
		return int64(l.lim.Limit())
	}
	return get(&s.up), get(&s.down)
}

// Conn counts c against the limits.
func (s *Shaper) Conn(c net.Conn) net.Conn {
	ctx, cancel := context.WithCancel(context.Background())
	return &shapedConn{Conn: c, s: s, ctx: ctx, cancel: cancel}
}

// PacketConn counts pc's datagrams against the limits.
func (s *Shaper) PacketConn(pc net.PacketConn) net.PacketConn {
	ctx, cancel := context.WithCancel(context.Background())
	return &shapedPacketConn{PacketConn: pc, s: s, ctx: ctx, cancel: cancel}
}

// A shaped connection's waits end when it is closed, so a paused Read or
// Write does not hold on past Close.
type shapedConn struct {
	net.Conn
	s      *Shaper
	ctx    context.Context
	cancel context.CancelFunc
}

func (c *shapedConn) Read(p []byte) (int, error) {
	if len(p) > shaperBurst && c.s.limited(pickDown) {
		p = p[:shaperBurst]
	}
	n, err := c.Conn.Read(p)
	c.s.wait(c.ctx, pickDown, n) // the bytes are read already; a closed wait only skips the pause
	return n, err
}

func (c *shapedConn) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		chunk := p[:min(len(p), shaperBurst)]
		if err := c.s.wait(c.ctx, pickUp, len(chunk)); err != nil {
			return written, err
		}
		n, err := c.Conn.Write(chunk)
		written += n
		if err != nil {
			return written, err
		}
		p = p[len(chunk):]
	}
	return written, nil
}

func (c *shapedConn) Close() error {
	c.cancel()
	return c.Conn.Close()
}

type shapedPacketConn struct {
	net.PacketConn
	s      *Shaper
	ctx    context.Context
	cancel context.CancelFunc
}

func (c *shapedPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(p)
	c.s.wait(c.ctx, pickDown, n) // as in shapedConn.Read
	return n, addr, err
}

func (c *shapedPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if err := c.s.wait(c.ctx, pickUp, len(p)); err != nil {
		return 0, err
	}
	return c.PacketConn.WriteTo(p, addr)
}

func (c *shapedPacketConn) Close() error {
	c.cancel()
	return c.PacketConn.Close()
}
