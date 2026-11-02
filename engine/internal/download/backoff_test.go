package download

import (
	"net/http"
	"testing"
	"time"
)

func TestPushBackCostsAConnectionAndPauses(t *testing.T) {
	b := backoff{conns: 4}
	b.pushedBack(start, 15*time.Second)
	if b.target() != 3 {
		t.Errorf("target %d after a push-back, want 3", b.target())
	}
	if b.mayOpen(start.Add(14 * time.Second)) {
		t.Error("opened a connection inside the Retry-After pause")
	}
	if !b.mayOpen(start.Add(15 * time.Second)) {
		t.Error("still paused after Retry-After")
	}
}

func TestQuietTwoMinutesGiveAConnectionBack(t *testing.T) {
	b := backoff{conns: 4}
	b.pushedBack(start, 15*time.Second)
	b.pushedBack(start, 15*time.Second)
	now := start.Add(2*time.Minute + time.Second)
	b.recover(now)
	b.recover(now)
	if b.target() != 3 {
		t.Fatalf("target %d after one quiet stretch, want 3", b.target())
	}
	b.recover(now.Add(2*time.Minute + time.Second))
	if b.target() != 4 {
		t.Fatalf("target %d after two quiet stretches, want 4", b.target())
	}
}

func TestThirdFailureInARowDecidesTheLink(t *testing.T) {
	for _, tc := range []struct {
		third errKind
		want  verdict
	}{{kindNet, linkUnreachable}, {kindHTTP, linkHTTPError}} {
		b := backoff{conns: 4}
		if v := b.failed(start, kindHTTP); v != keepGoing {
			t.Fatalf("first failure: %v, want keep going", v)
		}
		if b.mayOpen(start.Add(time.Second)) || !b.mayOpen(start.Add(2*time.Second)) {
			t.Error("a failure should pause new connections for 2s")
		}
		b.failed(start, kindNet)
		if v := b.failed(start, tc.third); v != tc.want {
			t.Errorf("third failure %v: %v, want %v", tc.third, v, tc.want)
		}
		if v := b.failed(start, kindNet); v != keepGoing {
			t.Errorf("the streak should start over after it decided the link, got %v", v)
		}
	}
}

func TestFinishedChunkEndsTheStreak(t *testing.T) {
	b := backoff{conns: 4}
	b.failed(start, kindNet)
	b.failed(start, kindNet)
	b.chunkDone()
	if v := b.failed(start, kindNet); v != keepGoing {
		t.Errorf("%v after a finished chunk, want keep going", v)
	}
}

func TestOnlyPushBackMakesTheServerBusy(t *testing.T) {
	b := backoff{conns: 4}
	b.failed(start, kindNet)
	if b.busy(start) {
		t.Error("a network failure isn't the server being busy")
	}
	b.pushedBack(start, 15*time.Second)
	b.failed(start.Add(time.Second), kindNet)
	if b.mayOpen(start.Add(10 * time.Second)) {
		t.Error("a network failure cut the Retry-After pause short")
	}
	b.recover(start.Add(3 * time.Minute))
	if !b.busy(start.Add(10*time.Second)) || b.busy(start.Add(3*time.Minute)) {
		t.Error("busy should last while a connection is cut or the Retry-After pause runs")
	}
}

func TestRetryAfter(t *testing.T) {
	for h, want := range map[string]time.Duration{
		"120": 2 * time.Minute,
		start.Add(90 * time.Second).Format(http.TimeFormat): 90 * time.Second,
		"9999": 10 * time.Minute,
		start.Add(time.Hour).Format(http.TimeFormat): 10 * time.Minute,
		"":     15 * time.Second,
		"soon": 15 * time.Second,
		"0":    15 * time.Second,
		start.Add(-time.Minute).Format(http.TimeFormat): 15 * time.Second,
	} {
		if got := retryAfter(h, start); got != want {
			t.Errorf("Retry-After %q: %v, want %v", h, got, want)
		}
	}
}

func TestPushBackKeepsOneConnection(t *testing.T) {
	b := backoff{conns: 1}
	b.pushedBack(start, 15*time.Second)
	b.pushedBack(start, 15*time.Second)
	if b.target() != 1 {
		t.Fatalf("target %d, want 1", b.target())
	}
	if !b.busy(start) || b.busy(start.Add(15*time.Second)) {
		t.Error("with nothing to cut, busy should last as long as the pause")
	}
}
