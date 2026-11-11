// Package queue is the downloads list: HTTP downloads and torrents in the
// order added or moved, which of them hold the Max parallel slots, and their saved state.
package queue

import (
	"cmp"
	"encoding/json"
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

	// saveMu orders the state files' writes: two Saves (the tick's and Close's)
	// never write one file at once, and a Remove never runs during a Save that
	// would write its file back.
	saveMu sync.Mutex

	mu    sync.Mutex
	byID  map[string]Entry
	order []string
	dirty map[string]bool
	held  map[string]bool // queued entries Schedule doesn't start (Hold); replaced whole, never changed
}

// New is a queue saving under dir, holding loaded in the order they were
// added, or as the user last moved them.
func New(dir string, loaded []Entry) *Queue {
	q := &Queue{dir: dir, byID: map[string]Entry{}, dirty: map[string]bool{}}
	loaded = slices.Clone(loaded)
	sort.SliceStable(loaded, func(i, j int) bool { return loaded[i].Added().Before(loaded[j].Added()) })
	// Entries the saved order doesn't know were added after the last move: they go last.
	var saved []string
	if b, err := os.ReadFile(q.orderPath()); err == nil {
		json.Unmarshal(b, &saved)
	}
	rank := func(e Entry) int {
		if i := slices.Index(saved, e.ID()); i >= 0 {
			return i
		}
		return len(saved)
	}
	sort.SliceStable(loaded, func(i, j int) bool { return rank(loaded[i]) < rank(loaded[j]) })
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

// List is every entry in the queue's order: as added, unless moved.
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

// Move puts the entry id just before the entry before, or last when before is
// "". The order is what Schedule starts by, and is kept across restarts.
// A queued entry moved before one that holds a slot takes that slot: the
// other goes back to the queue, next after it.
func (q *Queue) Move(id, before string) error {
	q.saveMu.Lock()
	defer q.saveMu.Unlock()
	q.mu.Lock()
	if q.byID[id] == nil || (before != "" && q.byID[before] == nil) {
		q.mu.Unlock()
		return fmt.Errorf("no download %q", cmp.Or(before, id))
	}
	if id != before {
		q.order = slices.DeleteFunc(q.order, func(x string) bool { return x == id })
		at := len(q.order)
		if before != "" {
			at = slices.Index(q.order, before)
		}
		q.order = slices.Insert(q.order, at, id)
	}
	order := slices.Clone(q.order)
	me, b := q.byID[id], q.byID[before]
	swap := b != nil && b != me && b.Running() && me.Status() == download.StatusQueued && !q.held[id]
	q.mu.Unlock()
	if swap {
		b.Stop(download.StatusQueued)
		me.Start()
	}
	return atomicfile.WriteJSON(q.orderPath(), order)
}

// Order puts the entries ids, wherever they are in the queue, in this order
// among the places they hold: the entries between them stay where they are.
func (q *Queue) Order(ids []string) {
	q.saveMu.Lock()
	defer q.saveMu.Unlock()
	q.mu.Lock()
	ids = slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return q.byID[id] == nil })
	changed, k := false, 0
	for i, id := range q.order {
		if k < len(ids) && slices.Contains(ids, id) {
			changed = changed || id != ids[k]
			q.order[i] = ids[k]
			k++
		}
	}
	order := slices.Clone(q.order)
	q.mu.Unlock()
	if !changed {
		return
	}
	if err := atomicfile.WriteJSON(q.orderPath(), order); err != nil {
		log.Printf("saving the queue's order: %v", err)
	}
}

// Hold keeps the queued entries ids from starting until a later Hold leaves
// them out: the parts of a split archive that wait for the part before them.
func (q *Queue) Hold(ids map[string]bool) {
	q.mu.Lock()
	q.held = ids
	q.mu.Unlock()
}

func (q *Queue) orderPath() string { return filepath.Join(q.dir, "order.json") }

// Remove discards an entry and its saved state.
func (q *Queue) Remove(id string, deleteFiles bool) error {
	e, err := q.Get(id)
	if err != nil {
		return err
	}
	e.Discard(deleteFiles)
	q.saveMu.Lock()
	defer q.saveMu.Unlock()
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
	q.stopAll(func(Entry) bool { return true }, download.StatusPaused)
}

// stopAll stops the entries which picks, at once: each Stop waits for its
// own connections to end, so one after another the waits would add up.
func (q *Queue) stopAll(which func(Entry) bool, as download.Status) {
	var wg sync.WaitGroup
	for _, e := range q.List() {
		if which(e) {
			wg.Go(func() { e.Stop(as) })
		}
	}
	wg.Wait()
}

func (q *Queue) ResumeAll() {
	for _, e := range q.List() {
		if s := e.Status(); s == download.StatusPaused || s == download.StatusFailed {
			e.Resume()
		}
	}
}

// Schedule starts queued entries while fewer than limit hold a slot, HTTP
// downloads before torrents, each in the queue's order. A queued HTTP download
// that finds no free slot takes one from the latest added torrent that is
// downloading, which goes back to the queue. Under VPN only, an entry waits
// while snap has no tunnel up.
func (q *Queue) Schedule(limit int, snap paths.Snapshot) {
	running := 0
	var queued, yielding, downloading []Entry // downloading: yielding entries holding a slot
	var holding []Entry                       // HTTP downloads holding a slot, oldest first
	q.mu.Lock()
	held := q.held
	q.mu.Unlock()
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
			} else {
				holding = append(holding, e)
			}
		case e.Rejoins():
			e.Start()
		case held[e.ID()]:
		case e.Status() == download.StatusQueued && e.Yields():
			yielding = append(yielding, e)
		case e.Status() == download.StatusQueued:
			queued = append(queued, e)
		}
	}
	// The limit went down: the newest give their slots back, torrents first.
	for running > limit {
		var e Entry
		if n := len(downloading); n > 0 {
			e, downloading = downloading[n-1], downloading[:n-1]
		} else if n := len(holding); n > 0 {
			e, holding = holding[n-1], holding[:n-1]
		} else {
			break
		}
		e.Stop(download.StatusQueued)
		running--
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
	q.saveMu.Lock()
	defer q.saveMu.Unlock()
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
	q.stopAll(Entry.Running, download.StatusQueued)
	for _, e := range q.List() {
		q.MarkDirty(e.ID())
	}
	q.Save()
}

func (q *Queue) statePath(e Entry) string {
	kind, _ := e.Saved()
	return filepath.Join(q.dir, kind, e.ID()+".json")
}
