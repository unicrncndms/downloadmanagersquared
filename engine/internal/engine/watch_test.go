package engine

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/ipc"
)

// watcher is a client whose events arrive on events, by name.
func watcher(t *testing.T, e *Engine) (*ipc.Conn, chan string) {
	t.Helper()
	ours, theirs := net.Pipe()
	t.Cleanup(func() { ours.Close(); theirs.Close() })
	events := make(chan string, 16)
	go func() {
		r := bufio.NewReader(theirs)
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				return
			}
			var ev struct {
				Event string `json:"event"`
			}
			if json.Unmarshal(line, &ev) == nil && ev.Event != "" {
				events <- ev.Event
			}
		}
	}()
	c := ipc.NewConn(ours)
	e.Connected(c)
	return c, events
}

func next(t *testing.T, events chan string) string {
	t.Helper()
	select {
	case ev := <-events:
		return ev
	case <-time.After(2 * time.Second):
		return ""
	}
}

func silent(t *testing.T, events chan string) {
	t.Helper()
	select {
	case ev := <-events:
		t.Fatalf("unexpected %q event", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

// A client that said unwatch gets the badge event instead of the state, and
// the state again, at once, when it watches.
func TestUnwatchedClientsGetOnlyTheBadge(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	_, legacyEvents := watcher(t, e) // never says either: watches
	quiet, quietEvents := watcher(t, e)
	if _, err := e.Call(quiet, "unwatch", nil); err != nil {
		t.Fatal(err)
	}

	var sent lastSent
	e.updateSnapshot(time.Second)
	e.push(&sent)
	if ev := next(t, legacyEvents); ev != "state" {
		t.Fatalf("a watcher got %q, want state", ev)
	}
	if ev := next(t, quietEvents); ev != "badge" {
		t.Fatalf("an unwatched client got %q, want badge", ev)
	}
	e.push(&sent) // nothing changed
	silent(t, legacyEvents)
	silent(t, quietEvents)

	if _, err := e.Call(quiet, "watch", nil); err != nil {
		t.Fatal(err)
	}
	if ev := next(t, quietEvents); ev != "state" {
		t.Fatalf("watching gave %q, want the state at once", ev)
	}
	e.push(&sent)
	silent(t, quietEvents)

	// Unwatching sends the badge again at the next tick, even unchanged.
	if _, err := e.Call(quiet, "unwatch", nil); err != nil {
		t.Fatal(err)
	}
	e.push(&sent)
	if ev := next(t, quietEvents); ev != "badge" {
		t.Fatalf("after unwatch got %q, want badge", ev)
	}
}
