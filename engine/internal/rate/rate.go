// Package rate turns a growing byte counter into a smoothed speed.
package rate

import "time"

type sample struct {
	at    time.Time
	total int64
}

// Meter averages throughput over a sliding window, so estimates stay calm.
type Meter struct {
	Window  time.Duration
	samples []sample
}

// Add records the counter's value at time t.
func (m *Meter) Add(t time.Time, total int64) {
	m.samples = append(m.samples, sample{t, total})
	cut := 0
	for cut < len(m.samples)-2 && t.Sub(m.samples[cut+1].at) >= m.Window {
		cut++
	}
	m.samples = m.samples[cut:]
}

// Rate is bytes per second across the window (or whatever part of it is filled).
func (m *Meter) Rate() float64 {
	if len(m.samples) < 2 {
		return 0
	}
	first, last := m.samples[0], m.samples[len(m.samples)-1]
	dt := last.at.Sub(first.at).Seconds()
	if dt <= 0 {
		return 0
	}
	return float64(last.total-first.total) / dt
}

func (m *Meter) Reset() { m.samples = nil }

// EWMA is a short-term speed for live per-interface display.
type EWMA struct {
	last  int64
	value float64
	init  bool
}

// Update feeds the counter's value after interval dt and returns the new speed.
func (e *EWMA) Update(total int64, dt time.Duration) float64 {
	if !e.init {
		e.last, e.init = total, true
		return 0
	}
	inst := float64(total-e.last) / dt.Seconds()
	e.last = total
	e.value = 0.3*inst + 0.7*e.value
	return e.value
}

func (e *EWMA) Value() float64 { return e.value }

// ETA is a time-left estimate that only moves every 5 seconds, so it doesn't jitter.
type ETA struct {
	secs float64
	ok   bool
	at   time.Time
}

// Update recomputes the estimate from the bytes left and the current rate, at most every 5s.
func (e *ETA) Update(now time.Time, remaining int64, rate float64) {
	if now.Sub(e.at) < 5*time.Second {
		return
	}
	e.at = now
	e.ok = remaining > 0 && rate > 0
	if e.ok {
		e.secs = float64(remaining) / rate
	}
}

// Clear makes the estimate unknown until the next Update.
func (e *ETA) Clear() { e.ok = false }

// Seconds is the estimate, -1 when unknown.
func (e *ETA) Seconds() float64 {
	if !e.ok {
		return -1
	}
	return e.secs
}
