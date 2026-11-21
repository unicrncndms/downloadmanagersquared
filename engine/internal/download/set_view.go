package download

import (
	"fmt"
	"math"
	"time"

	"downloadmanagersquared/engine/internal/archive"
)

// What a Set shows its parts: the SetSummary and its progress meter.
// Split from set.go.

// SetSummary is what the parts' summaries show of their Set.
type SetSummary struct {
	Parts    int    `json:"parts,omitempty"`   // how many the archive has, once known
	Missing  []int  `json:"missing,omitempty"` // parts not added, by number
	Note     string `json:"note,omitempty"`    // "Part 3 missing", or why it isn't extracted
	Files    int    `json:"files"`
	Of       int    `json:"of"`
	Out      string `json:"out,omitempty"`
	Finished bool   `json:"finished,omitempty"`
	Locked   bool   `json:"locked,omitempty"`  // a part's bytes are freed: archive keeping can't change any more
	Failing  bool   `json:"failing,omitempty"` // the extraction failed and tries again by itself; Note says why

	// The extraction's progress: bytes of the archive extracted (freed or kept), of
	// how many (a part not started yet counts as long as part 1), what it is
	// doing or waiting for, and the seconds left at its pace, 0 while unknown.
	Extracted int64   `json:"extracted,omitempty"`
	Total     int64   `json:"total,omitempty"`
	Phase     string  `json:"phase,omitempty"`
	ETA       float64 `json:"eta,omitempty"`
}

// meter is how fast a count grows, over its last few seconds.
type meter struct {
	at     time.Time
	n      int64
	perSec float64
}

func (m *meter) add(now time.Time, n int64) {
	if m.at.IsZero() || n < m.n {
		m.at, m.n = now, n
		return
	}
	if dt := now.Sub(m.at).Seconds(); dt >= 3 {
		v := float64(n-m.n) / dt
		if m.perSec == 0 {
			m.perSec = v
		} else {
			m.perSec = 0.7*m.perSec + 0.3*v
		}
		m.at, m.n = now, n
	}
}

// left is the seconds until the count reaches total, 0 while unknown.
func (m *meter) left(total int64) float64 {
	if m.perSec < 1 || total <= m.n {
		return 0
	}
	return float64(total-m.n) / m.perSec
}

// publish updates what the parts' summaries show.
func (s *Set) publish(run *setRun) {
	var cost int64
	if run.ex != nil {
		cost = run.ex.streaming.Load()
	}
	s.cost.Store(cost)
	s.tail.Store(run.tail)
	// A RAR is one stream, and so is an archive whose largest Entry is longer
	// than a part: read front to back only. Not known before its list is read.
	arc := run.arc()
	s.inOrder.Store(arc == nil || s.rar || run.parts != nil && arc.LargestCompressed > run.parts.Span(0).End)
	v := &SetSummary{Note: run.note, Failing: run.failing, Out: run.out, Finished: run.finished, Locked: run.locked, Phase: run.phase, Extracted: run.extracted, Total: run.sizes}
	if run.tail != nil && v.Phase == "" {
		v.Phase = "Waiting for the archive's list of files"
	}
	if run.finished {
		v.Extracted, v.Phase = v.Total, ""
	}
	run.pace.add(time.Now(), v.Extracted)
	if run.parts != nil {
		v.Parts = run.parts.Len()
		s.mu.Lock()
		for i := 1; i <= v.Parts; i++ {
			if s.members[i] == nil && !(i == v.Parts && s.members[archive.LastPart] != nil) {
				v.Missing = append(v.Missing, i)
			}
		}
		s.mu.Unlock()
	}
	v.Files, v.Of = run.files1, run.of
	if run.ex != nil && run.ex.x != nil {
		v.Files, v.Of = run.ex.x.Progress()
	}
	old := s.view.Load()
	changed := old == nil
	if old != nil {
		o := *old
		o.ETA = 0 // the time left alone isn't a change to save
		changed = fmt.Sprint(o) != fmt.Sprint(*v)
	}
	if !run.finished {
		v.ETA = math.Round(run.pace.left(v.Total))
	}
	s.view.Store(v)
	if changed {
		s.mu.Lock()
		for _, d := range s.members {
			d.changed()
		}
		s.mu.Unlock()
	}
}

// Summary is what the parts show of the Set, nil before it has run.
func (s *Set) Summary() *SetSummary { return s.view.Load() }
