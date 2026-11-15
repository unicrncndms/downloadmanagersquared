// Package dnscache remembers what a lookup answered for a short while, so the
// connections of one download share one lookup. A Binding and a Tunnel each
// own one: an answer is only good on the path that got it.
package dnscache

import (
	"context"
	"net"
	"sync"
	"time"
)

// TTL is how long an answer is kept.
const TTL = 45 * time.Second

// Cache is safe for concurrent use. The zero value is ready; Now is for tests.
type Cache struct {
	Now func() time.Time

	mu     sync.Mutex
	hits   map[string]*entry
	rotate uint
}

type entry struct {
	ips  []net.IP
	at   time.Time
	done chan struct{} // closed when the lookup that fills it ends
	err  error
}

// Lookup answers key from the cache, or from look, which runs once for
// connections that ask together. Each answer starts at the next address, so
// connections spread over a host's addresses. Failures are not kept.
func (c *Cache) Lookup(ctx context.Context, key string, look func(context.Context) ([]net.IP, error)) ([]net.IP, error) {
	for {
		c.mu.Lock()
		if c.hits == nil {
			c.hits = map[string]*entry{}
		}
		e := c.hits[key]
		switch {
		case e == nil || (e.done == nil && c.now().Sub(e.at) > TTL):
			e = &entry{done: make(chan struct{})}
			c.hits[key] = e
			c.mu.Unlock()
			// The lookup must not die with the first caller's context.
			e.ips, e.err = look(context.WithoutCancel(ctx))
			c.mu.Lock()
			e.at = c.now()
			if e.err != nil || len(e.ips) == 0 {
				delete(c.hits, key)
			}
			close(e.done)
			e.done = nil
			c.mu.Unlock()
			return c.answer(e)
		case e.done != nil:
			done := e.done
			c.mu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if e.err != nil {
				return nil, e.err
			}
			continue
		}
		c.mu.Unlock()
		return c.answer(e)
	}
}

// answer is a copy of e's addresses, rotated by one more than the last answer.
func (c *Cache) answer(e *entry) ([]net.IP, error) {
	if e.err != nil {
		return nil, e.err
	}
	c.mu.Lock()
	n := int(c.rotate % uint(max(len(e.ips), 1)))
	c.rotate++
	c.mu.Unlock()
	out := make([]net.IP, 0, len(e.ips))
	out = append(append(out, e.ips[n:]...), e.ips[:n]...)
	return out, nil
}

func (c *Cache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
