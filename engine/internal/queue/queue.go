// Package queue is the downloads list: HTTP downloads and torrents in the
// order added, which of them hold the Max parallel slots, and their saved state.
package queue

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"downloadmanagersquared/engine/internal/atomicfile"
	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/paths"
)

// Entry is an HTTP download or a torrent as the queue sees it.
type Entry interface {
	ID() string
	Status() download.Status
	Running() bool // holds a slot
	Start()
	Stop(as download.Status)
	Path() string
	Summary() download.Summary
	Added() time.Time
	VPN() paths.Mode
	PauseForVPN()
	WaitingForVPN() bool

	// Resume lets a stopped entry run again.
	Resume()
	// Discard stops the entry for good, deleting its files if asked and they
	// aren't finished yet.
	Discard(deleteFiles bool)
	// Yields: queues behind HTTP downloads and gives its slot to one.
	Yields() bool
	// Rejoins: should start now without taking a slot.
	Rejoins() bool
	// Saved is the entry's kind, which names its state directory, and its state.
	Saved() (kind string, state any)
}

type Queue struct {
	dir string

	mu    sync.Mutex
	byID  map[string]Entry
	order []string
	dirty map[string]bool
}

// New is a queue saving under dir, holding loaded in the order they were added.
func New(dir string, loaded []Entry) *Queue {
	q := &Queue{dir: dir, byID: map[string]Entry{}, dirty: map[string]bool{}}
	loaded = slices.Clone(loaded)
	sort.SliceStable(loaded, func(i, j int) bool { return loaded[i].Added().Before(loaded[j].Added()) })
	for _, e := range loaded {
		q.byID[e.ID()] = e
		q.order = append(q.order, e.ID())
	}
	return q
}

// Add puts a new entry at the end of the queue.
func (q *Queue) Add(e Entry) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.byID[e.ID()] = e
	q.order = append(q.order, e.ID())
	q.dirty[e.ID()] = true
}

func (q *Queue) Get(id string) (Entry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	e := q.byID[id]
	if e == nil {
		return nil, fmt.Errorf("no download %q", id)
	}
	return e, nil
}

// List is every entry in the order added.
func (q *Queue) List() []Entry {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Entry, 0, len(q.order))
	for _, id := range q.order {
		out = append(out, q.byID[id])
	}
	return out
}

// Replace swaps old for e in its place, unless old was replaced or removed meanwhile.
func (q *Queue) Replace(old, e Entry) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.byID[old.ID()] != old {
		return fmt.Errorf("%q changed meanwhile", old.ID())
	}
	q.byID[e.ID()], q.dirty[e.ID()] = e, true
	return nil
}

// Remove discards an entry and its saved state.
func (q *Queue) Remove(id string, deleteFiles bool) error {
	e, err := q.Get(id)
	if err != nil {
		return err
	}
	e.Discard(deleteFiles)
	q.mu.Lock()
	delete(q.byID, id)
	delete(q.dirty, id)
	q.order = slices.DeleteFunc(q.order, func(x string) bool { return x == id })
	q.mu.Unlock()
	os.Remove(q.statePath(e))
	return nil
}

func (q *Queue) Pause(id string) error {
	e, err := q.Get(id)
	if err != nil {
		return err
	}
	e.Stop(download.StatusPaused)
	return nil
}

func (q *Queue) Resume(id string) error {
	e, err := q.Get(id)
	if err != nil {
		return err
	}
	if !e.Running() {
		e.Resume()
	}
	return nil
}

func (q *Queue) PauseAll() {
	for _, e := range q.List() {
		e.Stop(download.StatusPaused)
	}
}

func (q *Queue) ResumeAll() {
	for _, e := range q.List() {
		if s := e.Status(); s == download.StatusPaused || s == download.StatusFailed {
			e.Resume()
		}
	}
}

// Schedule starts queued entries while fewer than limit hold a slot, HTTP
// downloads before torrents, each in the order added. A queued HTTP download
// that finds no free slot takes one from the latest added torrent that is
// downloading, which goes back to the queue. Under VPN only, an entry waits
// while snap has no tunnel up.
func (q *Queue) Schedule(limit int, snap paths.Snapshot) {
	running := 0
	var queued, yielding, downloading []Entry // downloading: yielding entries holding a slot
	for _, e := range q.List() {
		if paths.For(e.VPN(), snap).Wait {
			e.PauseForVPN() // never without a tunnel: under VPN only there is no other path
			if e.WaitingForVPN() {
				continue
			}
		} else if e.WaitingForVPN() {
			e.Resume() // a tunnel came up
		}
		switch {
		case e.Running():
			running++
			if e.Yields() {
				downloading = append(downloading, e)
			}
		case e.Rejoins():
			e.Start()
		case e.Status() == download.StatusQueued && e.Yields():
			yielding = append(yielding, e)
		case e.Status() == download.StatusQueued:
			queued = append(queued, e)
		}
	}
	for _, e := range append(queued, yielding...) {
		if running >= limit {
			if e.Yields() || len(downloading) == 0 {
				return
			}
			downloading[len(downloading)-1].Stop(download.StatusQueued)
			downloading = downloading[:len(downloading)-1]
			running--
		}
		e.Start()
		if e.Running() { // a torrent still to be chosen only fetches its files
			running++
		}
	}
}

// MarkDirty notes that an entry changed, to be saved by the next Save.
func (q *Queue) MarkDirty(id string) {
	q.mu.Lock()
	q.dirty[id] = true
	q.mu.Unlock()
}

// Save writes the state of every entry changed since the last Save.
func (q *Queue) Save() {
	q.mu.Lock()
	var changed []Entry
	for id := range q.dirty {
		if e := q.byID[id]; e != nil {
			changed = append(changed, e)
		}
	}
	q.dirty = map[string]bool{}
	q.mu.Unlock()
	for _, e := range changed {
		_, st := e.Saved()
		if err := atomicfile.WriteJSON(q.statePath(e), st); err != nil {
			log.Printf("saving %s: %v", e.ID(), err)
		}
	}
}

// Close stops every running entry, to queue again at the next start, and saves them all.
func (q *Queue) Close() {
	for _, e := range q.List() {
		if e.Running() {
			e.Stop(download.StatusQueued)
		}
		q.MarkDirty(e.ID())
	}
	q.Save()
}

func (q *Queue) statePath(e Entry) string {
	kind, _ := e.Saved()
	return filepath.Join(q.dir, kind, e.ID()+".json")
}
