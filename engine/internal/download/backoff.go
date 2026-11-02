package download

import (
	"net/http"
	"strconv"
	"time"
)

const maxFailsInARow = 3 // failures with no finished chunk in between before the link itself is in doubt

// verdict is what a failure means for the interface's link.
type verdict int

const (
	keepGoing       verdict = iota
	linkUnreachable         // the network is out: Link.unreachable
	linkHTTPError           // the server keeps erroring: Link.httpError
)

// backoff is how far one interface holds back. Its link stays ready meanwhile.
type backoff struct {
	conns       int // connections the settings allow
	cut         int // connections given up after the server pushed back
	lastCut     time.Time
	pausedUntil time.Time // no new connections before this
	busyUntil   time.Time // end of the server's Retry-After pause
	fails       int       // failures since the last finished chunk
}

// target is how many connections the interface may have open.
func (b *backoff) target() int { return max(1, b.conns-b.cut) }

// pushedBack: the server said to slow down. Give up a connection and open no
// new ones until Retry-After is over.
func (b *backoff) pushedBack(now time.Time, retryAfter time.Duration) {
	b.cut = min(b.cut+1, max(b.conns-1, 0))
	b.lastCut = now
	b.busyUntil = now.Add(retryAfter)
	b.pauseUntil(b.busyUntil)
}

// failed: a connection broke or got an HTTP error. Wait a moment before the
// next one; the third failure in a row decides about the link.
func (b *backoff) failed(now time.Time, kind errKind) verdict {
	b.pauseUntil(now.Add(2 * time.Second))
	b.fails++
	if b.fails < maxFailsInARow {
		return keepGoing
	}
	b.fails = 0
	if kind == kindHTTP {
		return linkHTTPError
	}
	return linkUnreachable
}

// chunkDone: data came through, so the failure streak is over.
func (b *backoff) chunkDone() { b.fails = 0 }

// recover gives a connection back after each quiet two minutes.
func (b *backoff) recover(now time.Time) {
	if b.cut > 0 && now.Sub(b.lastCut) > 2*time.Minute {
		b.cut--
		b.lastCut = now
	}
}

// mayOpen: new connections are allowed now.
func (b *backoff) mayOpen(now time.Time) bool { return !now.Before(b.pausedUntil) }

// busy: the server pushed back and the interface is still holding back for it.
func (b *backoff) busy(now time.Time) bool { return b.cut > 0 || now.Before(b.busyUntil) }

// retryAfter reads a Retry-After header, given in seconds or as a date.
func retryAfter(h string, now time.Time) time.Duration {
	var d time.Duration
	if s, err := strconv.Atoi(h); err == nil {
		d = time.Duration(s) * time.Second
	} else if t, err := http.ParseTime(h); err == nil {
		d = t.Sub(now)
	}
	if d <= 0 {
		return 15 * time.Second
	}
	return min(d, 10*time.Minute)
}

// pauseUntil only ever lengthens the pause.
func (b *backoff) pauseUntil(t time.Time) {
	if t.After(b.pausedUntil) {
		b.pausedUntil = t
	}
}
