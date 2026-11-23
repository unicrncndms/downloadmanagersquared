package torrent

import (
	"context"
	"net"
	"net/url"
	"time"
)

// resolveUDPTrackers replaces each udp:// tracker's hostname with the addresses
// lookup finds for it. The tracker library resolves UDP hostnames with the system
// resolver, which would send the query out of the Windows default route; an IP
// literal needs no lookup, and the UDP tracker protocol carries no hostname.
// Trackers whose host doesn't resolve are left out, and returned in missed for
// a later try; so are tiers left empty.
func resolveUDPTrackers(ctx context.Context, tiers [][]string, lookup func(context.Context, string) ([]net.IP, error)) (out [][]string, missed []string) {
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
				missed = append(missed, tr)
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
	return out, missed
}

// The waits between tries: the first, doubling up to the longest. Tests shorten them.
var (
	trackerRetryFirst = 5 * time.Second
	trackerRetryMax   = 5 * time.Minute
)

// resolveLater keeps trying the UDP trackers whose host didn't resolve when the
// torrent joined, as on a tunnel that has only just come up, and adds each one
// that resolves (add). Without this the torrent would never announce to them.
func resolveLater(ctx context.Context, missed []string, lookup func(context.Context, string) ([]net.IP, error), add func([][]string)) {
	wait := trackerRetryFirst
	for len(missed) > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		var got [][]string
		got, missed = resolveUDPTrackers(ctx, [][]string{missed}, lookup)
		if len(got) > 0 {
			add(got)
		}
		wait = min(wait*2, trackerRetryMax)
	}
}
