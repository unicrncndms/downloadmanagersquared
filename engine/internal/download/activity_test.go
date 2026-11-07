package download

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func texts(evs []Event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Iface+"|"+e.Text)
	}
	return out
}

// The log records what changed between summaries, once per change, with the
// interface it concerns.
func TestActivityLogRecordsChanges(t *testing.T) {
	d := New(nil, State{ID: "a", URL: "https://files.example/f.iso", Status: StatusQueued,
		Links: map[string]*Link{"eth": {URL: "https://files.example/f.iso", Status: LinkPending}}})
	d.Summary()
	d.Summary() // nothing changed: nothing logged again
	if n := len(d.Log()); n != 2 {
		t.Fatalf("a new download logs its state once: %v", texts(d.Log()))
	}

	d.mu.Lock()
	d.st.Links["eth"].expired("link expired (HTTP 403)")
	d.st.Links["wifi"] = &Link{Status: LinkResolving}
	d.mu.Unlock()
	d.Summary()
	got := texts(d.Log()[2:])
	if len(got) != 2 || !strings.HasPrefix(got[0], "eth|") && !strings.HasPrefix(got[1], "eth|") {
		t.Fatalf("want an event per changed link: %v", got)
	}
	if !strings.Contains(strings.Join(got, "\n"), "link expired (HTTP 403)") {
		t.Errorf("the link's error is in the log: %v", got)
	}

	d.mu.Lock()
	d.st.Status = StatusPaused
	d.mu.Unlock()
	d.Summary()
	last := d.Log()[len(d.Log())-1]
	if last.Iface != "" || last.Text == "" {
		t.Errorf("pausing is a download event: %+v", last)
	}
}

// Only the last maxEvents are kept, and they survive a restart without the
// current state being logged again.
func TestActivityLogKeepsLastAndSurvivesRestart(t *testing.T) {
	d := New(nil, State{ID: "a", URL: "https://x.example/f", Status: StatusQueued, Links: map[string]*Link{}})
	for i := range maxEvents + 20 {
		d.mu.Lock()
		d.st.Error = fmt.Sprintf("error %d", i)
		d.mu.Unlock()
		d.Summary()
	}
	log := d.Log()
	if len(log) != maxEvents || !strings.Contains(log[len(log)-1].Text, fmt.Sprintf("error %d", maxEvents+19)) {
		t.Fatalf("kept %d, last %q", len(log), log[len(log)-1].Text)
	}

	b, err := json.Marshal(d.State())
	if err != nil {
		t.Fatal(err)
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	d2 := New(nil, st)
	d2.Summary()
	if len(d2.Log()) != maxEvents {
		t.Fatalf("a restart logged the current state again: %d events", len(d2.Log()))
	}
}

// No query string (where tokens live) reaches the log, and cookies never do.
func TestActivityLogScrubsLinks(t *testing.T) {
	d := New(nil, State{ID: "a", URL: "https://x.example/f?token=secret", Status: StatusQueued, Cookie: "sid=hunter2",
		Links: map[string]*Link{"eth": {Status: LinkFailed, Error: `Get "https://cdn.example/f.iso?sig=abc&exp=1": EOF`}}})
	d.Summary()
	all := strings.Join(texts(d.Log()), "\n")
	for _, bad := range []string{"secret", "sig=abc", "hunter2"} {
		if strings.Contains(all, bad) {
			t.Errorf("%q leaked into the log: %s", bad, all)
		}
	}
	if !strings.Contains(all, "https://cdn.example/f.iso") {
		t.Errorf("the link itself stays: %s", all)
	}
}
