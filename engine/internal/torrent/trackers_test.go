package torrent

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"
)

func TestUDPTrackersAreAnnouncedToAddressesResolvedThroughTheInterface(t *testing.T) {
	lookup := func(_ context.Context, host string) ([]net.IP, error) {
		switch host {
		case "tracker.example":
			return []net.IP{net.IPv4(203, 0, 113, 7), net.IPv4(203, 0, 113, 8)}, nil
		case "other.example":
			return []net.IP{net.IPv4(198, 51, 100, 1)}, nil
		}
		return nil, errors.New("no such host")
	}
	got := resolveUDPTrackers(context.Background(), [][]string{
		{"udp://tracker.example:1337/announce", "https://web.example/announce"},
		{"udp://gone.example:80", "udp://203.0.113.9:6969"},
		{"udp://other.example:6969"},
	}, lookup)
	want := [][]string{
		// Every address of a host is a fallback in the same tier; HTTP keeps its
		// hostname because its dialer resolves through the interface already.
		{"udp://203.0.113.7:1337/announce", "udp://203.0.113.8:1337/announce", "https://web.example/announce"},
		// A host that doesn't resolve is left out rather than handed to the system resolver.
		{"udp://203.0.113.9:6969"},
		{"udp://198.51.100.1:6969"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}
