package dnscache

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func answer(ips ...string) func(context.Context) ([]net.IP, error) {
	return func(context.Context) ([]net.IP, error) {
		var out []net.IP
		for _, s := range ips {
			out = append(out, net.ParseIP(s))
		}
		return out, nil
	}
}

func TestAnswersAreKeptForTheTTLAndRotate(t *testing.T) {
	now := time.Now()
	c := &Cache{Now: func() time.Time { return now }}
	var lookups int
	look := func(ctx context.Context) ([]net.IP, error) {
		lookups++
		return answer("192.0.2.1", "192.0.2.2")(ctx)
	}
	var first []string
	for range 4 {
		ips, err := c.Lookup(t.Context(), "h", look)
		if err != nil || len(ips) != 2 {
			t.Fatal(ips, err)
		}
		first = append(first, ips[0].String())
	}
	if lookups != 1 || first[0] == first[1] || first[0] != first[2] {
		t.Fatalf("%d lookups, first addresses %v: want 1, alternating", lookups, first)
	}
	now = now.Add(TTL + time.Second)
	c.Lookup(t.Context(), "h", look)
	if lookups != 2 {
		t.Fatalf("%d lookups after the TTL, want 2", lookups)
	}
}

func TestFailuresAreNotKeptAndCachesAreSeparate(t *testing.T) {
	var a, b Cache
	if _, err := a.Lookup(t.Context(), "h", func(context.Context) ([]net.IP, error) { return nil, errors.New("down") }); err == nil {
		t.Fatal("no error")
	}
	if ips, err := a.Lookup(t.Context(), "h", answer("192.0.2.1")); err != nil || len(ips) != 1 {
		t.Fatalf("after a failure: %v %v", ips, err)
	}
	if ips, _ := b.Lookup(t.Context(), "h", answer("192.0.2.9")); ips[0].String() != "192.0.2.9" {
		t.Fatalf("another cache answered %v", ips)
	}
}

func TestConnectionsThatAskTogetherShareOneLookup(t *testing.T) {
	var c Cache
	var n atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			c.Lookup(t.Context(), "h", func(ctx context.Context) ([]net.IP, error) {
				n.Add(1)
				<-release
				return answer("192.0.2.1")(ctx)
			})
		})
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if n.Load() != 1 {
		t.Fatalf("%d lookups for 8 callers", n.Load())
	}
}
