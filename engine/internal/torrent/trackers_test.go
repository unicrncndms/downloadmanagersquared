package torrent

import (
	"context"
	"errors"
	"net"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
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
	got, missed := resolveUDPTrackers(context.Background(), [][]string{
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
	// The one left out is kept for a later try.
	if want := []string{"udp://gone.example:80"}; !reflect.DeepEqual(missed, want) {
		t.Fatalf("missed %q, want %q", missed, want)
	}
}

// A tracker that didn't resolve when the torrent joined (the tunnel had only
// just come up) is added once it does.
func TestATrackerThatDidntResolveIsAddedOnceItDoes(t *testing.T) {
	first := trackerRetryFirst
	trackerRetryFirst = 10 * time.Millisecond
	t.Cleanup(func() { trackerRetryFirst = first })
	var tries atomic.Int32
	lookup := func(_ context.Context, host string) ([]net.IP, error) {
		if tries.Add(1) < 3 {
			return nil, errors.New("no answer yet")
		}
		return []net.IP{net.IPv4(203, 0, 113, 7)}, nil
	}
	added := make(chan [][]string, 1)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	go resolveLater(ctx, []string{"udp://tracker.example:1337/announce"}, lookup, func(a [][]string) { added <- a })
	select {
	case got := <-added:
		if want := [][]string{{"udp://203.0.113.7:1337/announce"}}; !reflect.DeepEqual(got, want) {
			t.Fatalf("added %q, want %q", got, want)
		}
	case <-ctx.Done():
		t.Fatal("the tracker was never added")
	}
}
