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
