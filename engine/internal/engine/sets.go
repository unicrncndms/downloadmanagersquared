package engine

import (
	"downloadmanagersquared/engine/internal/download"
)

// syncSets puts the parts of each split archive in its Set: one starts with
// its first part and stops when its last part is removed.
func (e *Engine) syncSets() {
	e.setsMu.Lock()
	defer e.setsMu.Unlock()
	if e.sets == nil {
		e.sets = map[string]*download.Set{}
	}
	members := map[string]map[int]*download.Download{}
	names := map[string]string{}
	type lonePart struct {
		d    *download.Download
		part int
	}
	lone := map[string]lonePart{}
	for _, j := range e.queue.List() {
		d, ok := j.(*download.Download)
		if !ok {
			continue
		}
		key, set, part, alone := download.SetOf(d)
		if key == "" {
			continue
		}
		if alone {
			if _, ok := lone[key]; !ok {
				lone[key] = lonePart{d, part}
			}
			continue
		}
		if members[key] == nil {
			members[key], names[key] = map[int]*download.Download{}, set
		}
		if members[key][part] == nil {
			members[key][part] = d
		}
	}
	for key, l := range lone {
		// A plain x.rar or x.zip is part of a set only beside its x.r00… or x.z01…
		if m := members[key]; m != nil && m[l.part] == nil {
			m[l.part] = l.d
		}
	}
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
			s = download.NewSet(key, names[key])
			e.sets[key] = s
			s.SetMembers(m)
			s.Start()
			continue
		}
		s.SetMembers(m)
	}
}
