package queue

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/paths"
)

// fake is a download or torrent as the queue sees it.
type fake struct {
	id      string
	status  download.Status
	running bool
	yields  bool // a torrent
	rejoins bool // seeding: runs without a slot
	mode    paths.Mode
	waitVPN bool
	added   time.Time
	deleted bool
}

func (f *fake) ID() string                { return f.id }
func (f *fake) Status() download.Status   { return f.status }
func (f *fake) Running() bool             { return f.running }
func (f *fake) Path() string              { return "" }
func (f *fake) Summary() download.Summary { return download.Summary{ID: f.id, Status: f.status} }
func (f *fake) VPN() paths.Mode           { return f.mode }
func (f *fake) Yields() bool              { return f.yields }
func (f *fake) Rejoins() bool             { return f.rejoins && !f.running }
func (f *fake) Added() time.Time          { return f.added }
func (f *fake) WaitingForVPN() bool       { return f.status == download.StatusPaused && f.waitVPN }
func (f *fake) Resume()                   { f.Stop(download.StatusQueued) }
func (f *fake) Discard(deleteFiles bool)  { f.Stop(download.StatusPaused); f.deleted = deleteFiles }

func (f *fake) Saved() (string, any) {
	kind := "downloads"
	if f.yields {
		kind = "torrents"
	}
	return kind, map[string]string{"id": f.id}
}

func (f *fake) Start() {
	f.running, f.status = true, download.StatusRunning
}

func (f *fake) Stop(as download.Status) {
	f.running, f.status, f.waitVPN = false, as, false
}

func (f *fake) PauseForVPN() {
	if f.running || f.status == download.StatusQueued {
		f.running, f.status, f.waitVPN = false, download.StatusPaused, true
	}
}

var n int

func queued(yields bool) *fake {
	n++
	return &fake{id: string(rune('a' + n)), status: download.StatusQueued, yields: yields, mode: paths.Off,
		added: time.Unix(int64(n), 0)}
}

func newQueue(t *testing.T, es ...*fake) *Queue {
	t.Helper()
	q := New(t.TempDir(), nil)
	for _, e := range es {
		q.Add(e)
	}
	return q
}

var lo = paths.Snapshot{Ifaces: []paths.Iface{{ID: "lo", Present: true, Enabled: true}}}

func TestHTTPStartsBeforeAnEarlierQueuedTorrent(t *testing.T) {
	tor, http := queued(true), queued(false)
	newQueue(t, tor, http).Schedule(1, lo)
	if !http.running || tor.running {
		t.Fatalf("http running %v, torrent running %v; want the HTTP download first", http.running, tor.running)
	}
}

func TestHTTPTakesTheSlotOfTheLatestDownloadingTorrent(t *testing.T) {
	first, second := queued(true), queued(true)
	q := newQueue(t, first, second)
	q.Schedule(2, lo)
	http := queued(false)
	q.Add(http)
	q.Schedule(2, lo)
	if !http.running || !first.running || second.running || second.status != download.StatusQueued {
		t.Fatalf("http %v, first torrent %v, second %v (%q); want the latest torrent back in the queue",
			http.running, first.running, second.running, second.status)
	}
}

func TestSlotLimitHolds(t *testing.T) {
	a, b := queued(false), queued(false)
	newQueue(t, a, b).Schedule(1, lo)
	if !a.running || b.running {
		t.Fatalf("running %v and %v with one slot; want the first added only", a.running, b.running)
	}
}

func TestRejoiningEntryStartsWithoutASlot(t *testing.T) {
	seed := queued(true)
	seed.status, seed.rejoins = "seeding", true
	http := queued(false)
	newQueue(t, seed, http).Schedule(1, lo)
	if !seed.running || !http.running {
		t.Fatalf("seeding %v, http %v; want both, seeding holds no slot", seed.running, http.running)
	}
}

func TestVPNOnlyWaitsForATunnelThenQueuesAgain(t *testing.T) {
	d := queued(false)
	d.mode = paths.VPNOnly
	q := newQueue(t, d)
	q.Schedule(1, lo)
	if d.running || !d.WaitingForVPN() {
		t.Fatalf("with no tunnel: running %v, waiting %v", d.running, d.WaitingForVPN())
	}
	up := paths.Snapshot{Ifaces: []paths.Iface{{ID: "lo", Present: true, Enabled: true, TunnelUp: true}}}
	q.Schedule(1, up)
	if !d.running {
		t.Fatalf("a tunnel is up but the download is %q", d.status)
	}
}

func TestAUserPausedDownloadStaysPaused(t *testing.T) {
	d := queued(false)
	q := newQueue(t, d)
	q.Pause(d.id)
	q.Schedule(1, lo)
	if d.running {
		t.Fatal("a paused download started")
	}
	q.Resume(d.id)
	q.Schedule(1, lo)
	if !d.running {
		t.Fatalf("a resumed download is %q", d.status)
	}
}

func TestChangesAreSavedByKindAndRemovedWithTheEntry(t *testing.T) {
	dir := t.TempDir()
	q := New(dir, nil)
	http, tor := queued(false), queued(true)
	q.Add(http)
	q.Add(tor)
	q.Save()
	for _, p := range []string{filepath.Join(dir, "downloads", http.id+".json"), filepath.Join(dir, "torrents", tor.id+".json")} {
		b, err := os.ReadFile(p)
		var st map[string]string
		if err != nil || json.Unmarshal(b, &st) != nil {
			t.Fatalf("%s not saved: %v", p, err)
		}
	}
	if err := q.Remove(tor.id, true); err != nil {
		t.Fatal(err)
	}
	if !tor.deleted || len(q.List()) != 1 {
		t.Fatalf("removed torrent: files deleted %v, %d left in the queue", tor.deleted, len(q.List()))
	}
	if _, err := os.Stat(filepath.Join(dir, "torrents", tor.id+".json")); !os.IsNotExist(err) {
		t.Fatal("a removed torrent's state is still on disk")
	}
}

func TestLoadedEntriesComeBackInTheOrderAdded(t *testing.T) {
	a, b, c := queued(false), queued(true), queued(false)
	q := New(t.TempDir(), []Entry{c, a, b})
	var got []string
	for _, e := range q.List() {
		got = append(got, e.ID())
	}
	if want := []string{a.id, b.id, c.id}; len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("order %v, want %v", got, want)
	}
}

func TestLoweringTheLimitQueuesTheNewestRunning(t *testing.T) {
	a, b, c := queued(false), queued(false), queued(false)
	q := newQueue(t, a, b, c)
	q.Schedule(3, lo)
	if !a.running || !b.running || !c.running {
		t.Fatalf("all three should run at limit 3")
	}
	q.Schedule(1, lo)
	if !a.running || b.running || c.running || b.status != download.StatusQueued || c.status != download.StatusQueued {
		t.Fatalf("at limit 1 only the oldest runs; got a %v b %v/%s c %v/%s", a.running, b.running, b.status, c.running, c.status)
	}
	q.Schedule(1, lo)
	if !a.running || b.running || c.running {
		t.Fatal("the queued ones must stay queued at the same limit")
	}
}

func TestLoweringTheLimitQueuesATorrentBeforeAnHTTPDownload(t *testing.T) {
	h, tor := queued(false), queued(true)
	q := newQueue(t, h, tor)
	q.Schedule(2, lo)
	if !h.running || !tor.running {
		t.Fatalf("both should run at limit 2")
	}
	q.Schedule(1, lo)
	if !h.running || tor.running || tor.status != download.StatusQueued {
		t.Fatalf("the torrent gives up its slot first; got http %v torrent %v/%s", h.running, tor.running, tor.status)
	}
}

func TestAMovedEntryStartsFirstAndKeepsItsPlace(t *testing.T) {
	dir := t.TempDir()
	a, b, c := queued(false), queued(false), queued(false)
	q := New(dir, nil)
	for _, e := range []*fake{a, b, c} {
		q.Add(e)
	}
	if err := q.Move(c.id, a.id); err != nil {
		t.Fatal(err)
	}
	if err := q.Move(a.id, ""); err != nil {
		t.Fatal(err)
	}
	q.Schedule(1, lo)
	if !c.running || a.running || b.running {
		t.Fatalf("running a %v b %v c %v; want c, moved to the front", a.running, b.running, c.running)
	}
	// After a restart, with one added since the move: the moved order, then the new one.
	d := queued(false)
	var got []string
	for _, e := range New(dir, []Entry{d, a, b, c}).List() {
		got = append(got, e.ID())
	}
	if want := []string{c.id, b.id, a.id, d.id}; !slices.Equal(got, want) {
		t.Fatalf("order after restart %v, want %v", got, want)
	}
	if err := q.Move("nope", ""); err == nil {
		t.Fatal("moving an unknown download succeeded")
	}
}

func TestAQueuedEntryMovedBeforeARunningOneTakesItsSlot(t *testing.T) {
	a, b, c := queued(false), queued(false), queued(false)
	q := newQueue(t, a, b, c)
	q.Schedule(1, lo)
	if err := q.Move(c.id, a.id); err != nil {
		t.Fatal(err)
	}
	q.Schedule(1, lo)
	if !c.running || a.running || b.running || a.status != download.StatusQueued {
		t.Fatalf("running: a %v b %v c %v", a.running, b.running, c.running)
	}
	// A held entry (a split archive's part waiting its turn) takes no slot.
	q.Hold(map[string]bool{b.id: true})
	q.Move(b.id, c.id)
	if !c.running || b.running {
		t.Fatal("a held entry took a slot")
	}
}
