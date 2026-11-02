package torrent

import (
	"context"
	"net"
	"net/url"
)

// resolveUDPTrackers replaces each udp:// tracker's hostname with the addresses
// lookup finds for it. The tracker library resolves UDP hostnames with the system
// resolver, which would send the query out of the Windows default route; an IP
// literal needs no lookup, and the UDP tracker protocol carries no hostname.
// Trackers whose host doesn't resolve are dropped, and so are tiers left empty.
func resolveUDPTrackers(ctx context.Context, tiers [][]string, lookup func(context.Context, string) ([]net.IP, error)) [][]string {
	var out [][]string
	for _, tier := range tiers {
		var next []string
		for _, tr := range tier {
			u, err := url.Parse(tr)
			if err != nil || u.Scheme != "udp" {
				next = append(next, tr)
				continue
			}
			if net.ParseIP(u.Hostname()) != nil {
				next = append(next, tr)
				continue
			}
			ips, err := lookup(ctx, u.Hostname())
			if err != nil {
				continue
			}
			for _, ip := range ips {
				r := *u
				r.Host = net.JoinHostPort(ip.String(), u.Port())
				next = append(next, r.String())
			}
		}
		if len(next) > 0 {
			out = append(out, next)
		}
	}
	return out
}
