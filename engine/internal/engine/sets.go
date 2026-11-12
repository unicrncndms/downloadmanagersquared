package engine

import (
	"errors"
	"log"

	"downloadmanagersquared/engine/internal/download"
)

// RetryExtraction starts the extraction of the Set download id is a part of
// over: its run is rebuilt from the parts' record, as after a restart, so
// bytes it freed and still needs are fetched again.
func (e *Engine) RetryExtraction(id string) error {
	d, err := e.get(id)
	if err != nil {
		return err
	}
	e.setsMu.Lock()
	defer e.setsMu.Unlock()
	for _, s := range e.sets {
		if s.Holds(d) {
			log.Printf("set %s: extraction started over by the user", s.Key)
			s.Stop()
			s.Start()
			return nil
		}
	}
	return errors.New("it is not part of a split archive being extracted")
}

// syncSets puts the parts of each split archive in its Set: one starts with
// its first part and stops when its last part is removed. What the parts do
// meanwhile (who is in which Set, what waits, what goes first) is decided by
// download.PlanSets; this carries it out.
func (e *Engine) syncSets() {
	e.mu.Lock()
	parallel, saveDir := e.settings.PartsParallel, e.settings.SaveDir
	e.mu.Unlock()
	e.setsMu.Lock()
	defer e.setsMu.Unlock()
	if e.sets == nil {
		e.sets = map[string]*download.Set{}
	}
	var ds []*download.Download
	for _, j := range e.queue.List() {
		if d, ok := j.(*download.Download); ok {
			ds = append(ds, d)
		}
	}
	plan := download.PlanSets(ds, e.sets, parallel, saveDir, e.tails)
	e.tails = plan.Tails
	for _, a := range plan.Adopt {
		if a.D.SetFolder(true) == nil {
			a.D.SetFolderInto(a.Into)
		}
	}
	for _, d := range plan.Stop {
		d.Stop(download.StatusQueued)
	}
	for _, ids := range plan.Order {
		e.queue.Order(ids)
	}
	e.queue.Hold(plan.Hold)
	members := plan.Members
	for key, s := range e.sets {
		if members[key] == nil {
			s.SetMembers(nil)
			s.Stop()
			delete(e.sets, key)
		}
	}
	for key, m := range members {
		s := e.sets[key]
		if s == nil {
			s = download.NewSet(key, plan.Names[key])
			e.sets[key] = s
			s.SetMembers(m)
			s.Start()
			continue
		}
		s.SetMembers(m)
	}
}
