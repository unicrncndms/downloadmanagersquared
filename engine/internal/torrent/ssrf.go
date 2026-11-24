package torrent

import (
	"context"
	"errors"
	"net"
	"net/netip"
)

var errInternalTarget = errors.New("refused: a tracker or web seed address on this machine or a private network")

// cgnat is the carrier-grade NAT range, which IsPrivate leaves out.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// internalIP: loopback, private, link-local (cloud metadata), CGNAT, unspecified.
func internalIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsUnspecified() || a.IsMulticast() || cgnat.Contains(a)
}

// publicOnly wraps a dial for what a .torrent or magnet names (tracker and web seed
// URLs), so it can't make DMS² probe the user's own network. Peers don't go through it:
// LAN peers are fine. shortcut: no allow-list for a LAN tracker or web seed; add a
// setting if someone needs one.
func publicOnly(lookup func(context.Context, string) ([]net.IP, error),
	dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips := []net.IP{net.ParseIP(host)}
		if ips[0] == nil {
			if ips, err = lookup(ctx, host); err != nil {
				return nil, err
			}
		}
		for _, ip := range ips {
			if internalIP(ip) {
				return nil, errInternalTarget
			}
		}
		if len(ips) == 0 {
			return nil, errors.New("no address for " + host)
		}
		// Dial the checked address, so a second lookup can't answer differently.
		var last error
		for _, ip := range ips {
			c, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return c, nil
			}
			last = err
		}
		return nil, last
	}
}
