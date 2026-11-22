package engine

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"downloadmanagersquared/engine/internal/atomicfile"
	"downloadmanagersquared/engine/internal/download"
)

// What each connection manages alone (ticket 08): a time-weighted average of
// its speed over the time it fetched one HTTP download by itself and nothing
// held it back. It is shown on the connection's line.

const (
	baseKnownAfter = 120.0   // seconds counted before the average is shown or used
	baseCap        = 86400.0 // the average weighs the last day of counted time
	baseWarmup     = 5.0     // seconds a connection must have been clean before it counts: ramp-up is no speed
	baseSaveEvery  = time.Minute
)

type speedBase struct {
	Avg  float64 `json:"avg"`  // bytes per second
	Secs float64 `json:"secs"` // seconds counted, up to baseCap
}

func (b *speedBase) add(speed, secs float64) {
	b.Avg += (speed - b.Avg) * secs / (min(b.Secs, baseCap) + secs)
	b.Secs += secs
}

func (b speedBase) known() bool { return b.Secs >= baseKnownAfter && b.Avg > 0 }

type speedBases struct {
	path  string
	m     map[string]*speedBase
	clean map[string]float64 // seconds each connection has been clean for, not saved
	dirty bool
	saved time.Time
}

func openSpeedBases(dir string) *speedBases {
	s := &speedBases{path: filepath.Join(dir, "speeds.json"), m: map[string]*speedBase{}, clean: map[string]float64{}}
	if b, err := os.ReadFile(s.path); err == nil && json.Unmarshal(b, &s.m) != nil {
		log.Printf("speeds.json can't be read: starting without it")
		s.m = map[string]*speedBase{}
	}
	return s
}

// avg is the connection's average in bytes per second, 0 until it is known.
func (s *speedBases) avg(id string) float64 {
	if b := s.m[id]; b != nil && b.known() {
		return b.Avg
	}
	return 0
}

// learnSpeedsLocked counts this tick into each connection's average. Only a
// connection that fetches one HTTP download alone counts, and not while a
// speed limit, the server's backoff, a shared line or the server's own limit
// holds it back.
func (e *Engine) learnSpeedsLocked(sums []download.Summary, speeds map[string]float64, sameRouter map[string][]string, dt time.Duration) {
	b, secs := e.bases, dt.Seconds()
	if secs <= 0 {
		return
	}
	users, bad := map[string]int{}, map[string]bool{}
	for _, s := range sums {
		if s.Status != download.StatusRunning {
			continue
		}
		var links []linkNow
		for id, l := range s.Links {
			if b.avg(id) > 0 && l.Conns > 0 {
				links = append(links, linkNow{id, l.Speed, b.avg(id)})
			}
		}
		held := s.LimitedBy != "" || allWeak(links)
		for id, l := range s.Links {
			if l.Speed > 0 {
				users[id]++
				bad[id] = bad[id] || held || s.Kind == download.KindTorrent || l.Throttled()
			}
		}
	}
	for id := range e.ifaces {
		shared := false
		for _, o := range sameRouter[id] {
			shared = shared || speeds[o] > 0
		}
		if users[id] != 1 || bad[id] || shared || speeds[id] <= 0 {
			b.clean[id] = 0
			continue
		}
		if b.clean[id] += secs; b.clean[id] < baseWarmup {
			continue
		}
		if b.m[id] == nil {
			b.m[id] = &speedBase{}
		}
		b.m[id].add(speeds[id], secs)
		b.dirty = true
	}
	// shortcut: a stop within a minute of the last save loses that minute; flush on exit if that ever matters.
	if now := time.Now(); b.dirty && now.Sub(b.saved) >= baseSaveEvery {
		b.dirty, b.saved = false, now
		if err := atomicfile.WriteJSON(b.path, b.m); err != nil {
			log.Printf("speeds.json can't be saved: %v", err)
		}
	}
}

// A running connection with a known average.
type linkNow struct {
	ID          string
	Speed, Base float64
}

// allWeak: two or more connections, every one under half its average. That is
// the server sending no faster, so these samples say nothing about the lines.
func allWeak(links []linkNow) bool {
	if len(links) < 2 {
		return false
	}
	for _, l := range links {
		if l.Speed >= 0.5*l.Base {
			return false
		}
	}
	return true
}
