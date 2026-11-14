package queue

import (
	"maps"
	"time"

	"downloadmanagersquared/engine/internal/download"
)

// Span is one weekday's window, local "HH:MM" times; empty (or invalid, or
// Start == End) means no window that day. End before Start crosses midnight
// into the next day.
type Span struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// Week holds a Span per weekday, indexed like time.Weekday (Sunday first).
type Week [7]Span

func minutes(s string) (int, bool) {
	t, err := time.Parse("15:04", s)
	return t.Hour()*60 + t.Minute(), err == nil
}

// Valid is the span's start and end in minutes, and whether it is a window.
func (s Span) Valid() (start, end int, ok bool) {
	a, ok1 := minutes(s.Start)
	b, ok2 := minutes(s.End)
	return a, b, ok1 && ok2 && a != b
}

// Set says whether any weekday has a window.
func (w Week) Set() bool {
	for _, s := range w {
		if _, _, ok := s.Valid(); ok {
			return true
		}
	}
	return false
}

// Open says whether the queue may run at t. With no window on any day it
// always may; otherwise only inside one, today's or the one that began
// yesterday and crosses midnight.
func (w Week) Open(t time.Time) bool {
	if !w.Set() {
		return true
	}
	m := t.Hour()*60 + t.Minute()
	if a, b, ok := w[t.Weekday()].Valid(); ok {
		if a < b && m >= a && m < b || a > b && m >= a {
			return true
		}
	}
	if a, b, ok := w[(t.Weekday()+6)%7].Valid(); ok && a > b && m < b {
		return true
	}
	return false
}

// SetWindow tells the queue whether its window is open. Closing stops what
// runs or seeds, except entries started by hand, and remembers them; opening
// resumes those still paused. While closed, Schedule starts nothing but
// entries started by hand.
func (q *Queue) SetWindow(open bool) {
	q.mu.Lock()
	was := !q.closed
	q.closed = !open
	now := maps.Clone(q.now)
	q.mu.Unlock()
	if open == was {
		return
	}
	for _, e := range q.List() {
		id := e.ID()
		if open {
			q.mu.Lock()
			wp := q.winPaused[id]
			delete(q.winPaused, id)
			q.mu.Unlock()
			if wp && e.Status() == download.StatusPaused {
				e.Resume()
			}
		} else if !now[id] && (e.Running() || e.Status() == download.StatusSeeding) {
			q.mu.Lock()
			if q.winPaused == nil {
				q.winPaused = map[string]bool{}
			}
			q.winPaused[id] = true
			q.mu.Unlock()
			e.Stop(download.StatusPaused)
		}
	}
}
