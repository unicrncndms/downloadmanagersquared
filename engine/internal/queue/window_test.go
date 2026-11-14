package queue

import (
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/download"
)

func at(day, hm string) time.Time { // 2026-11-08 is a Sunday
	d := map[string]int{"sun": 8, "mon": 9, "tue": 10}[day]
	h, _ := time.Parse("15:04", hm)
	return time.Date(2026, 11, d, h.Hour(), h.Minute(), 0, 0, time.Local)
}

func TestWindowOpen(t *testing.T) {
	var none Week
	if !none.Open(at("mon", "12:00")) {
		t.Fatal("no window set must always be open")
	}
	w := Week{time.Sunday: {"22:00", "02:00"}, time.Monday: {"01:00", "07:00"}}
	for _, c := range []struct {
		day, hm string
		want    bool
	}{
		{"sun", "21:59", false}, {"sun", "22:00", true}, {"sun", "23:59", true},
		{"mon", "00:00", true}, {"mon", "01:59", true}, // Sunday's crosses midnight
		{"mon", "00:59", true}, {"mon", "01:00", true},
		{"mon", "06:59", true}, {"mon", "07:00", false},
		{"tue", "03:00", false}, // Tuesday has none, and Monday's does not cross
	} {
		if got := w.Open(at(c.day, c.hm)); got != c.want {
			t.Errorf("%s %s: open = %v, want %v", c.day, c.hm, got, c.want)
		}
	}
	if (Week{time.Monday: {"07:00", "07:00"}}).Set() || (Week{time.Monday: {"x", "07:00"}}).Set() {
		t.Error("an empty or invalid span is no window")
	}
}

func TestSetWindow(t *testing.T) {
	run, hand, mine := queued(false), queued(false), queued(false)
	q := newQueue(t, run, hand, mine)
	run.Start()
	hand.Start()
	mine.Stop(download.StatusPaused) // paused by the user
	q.mu.Lock()
	q.now = map[string]bool{hand.id: true}
	q.mu.Unlock()
	q.SetWindow(false)
	if run.status != download.StatusPaused || !hand.running {
		t.Fatalf("closing: run %v, hand running %v", run.status, hand.running)
	}
	late := queued(false)
	q.Add(late)
	q.Schedule(4, lo)
	if late.running {
		t.Fatal("nothing starts while the window is closed")
	}
	q.SetWindow(true)
	if run.status != download.StatusQueued || mine.status != download.StatusPaused {
		t.Fatalf("opening: run %v, user-paused %v", run.status, mine.status)
	}
	q.Schedule(4, lo)
	if !late.running {
		t.Fatal("opening lets the queue start")
	}
}

func TestResumeByHandWhileClosed(t *testing.T) {
	e := queued(false)
	e.status = download.StatusPaused
	q := newQueue(t, e)
	q.SetWindow(false)
	q.Resume(e.id)
	q.Schedule(4, lo)
	if !e.running {
		t.Fatal("a resume by hand outside the window runs")
	}
}
