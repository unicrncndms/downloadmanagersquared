package engine

import (
	"log"
	"time"

	"downloadmanagersquared/engine/internal/ipc"
)

// TickTiming turns on the tick timing log (DMS_PROFILE=1, see CONTRIBUTING.md).
var TickTiming bool

var tickPhases = [...]string{"prepare", "snapshot", "push", "save"}

// tickTimer times the phases of the 1 s tick and, at most once every 10 s,
// logs the slowest of each since the last line, with the size of the state.
type tickTimer struct {
	mark    time.Time
	max     [len(tickPhases)]time.Duration
	lastLog time.Time
}

func (m *tickTimer) begin() {
	if TickTiming {
		m.mark = time.Now()
	}
}

func (m *tickTimer) phase(i int) {
	if !TickTiming {
		return
	}
	now := time.Now()
	m.max[i] = max(m.max[i], now.Sub(m.mark))
	m.mark = now
}

func (m *tickTimer) log(e *Engine) {
	if !TickTiming || m.mark.Sub(m.lastLog) < 10*time.Second {
		return
	}
	m.lastLog = m.mark
	size := 0 // encoded apart from push, which skips it with no client watching
	if b, err := ipc.EncodeEvent("state", e.Snapshot()); err == nil {
		size = len(b)
	}
	log.Printf("tick, slowest of the last 10 s: %s %v, %s %v, %s %v, %s %v; state %d bytes",
		tickPhases[0], m.max[0], tickPhases[1], m.max[1], tickPhases[2], m.max[2], tickPhases[3], m.max[3], size)
	m.max = [len(tickPhases)]time.Duration{}
}
