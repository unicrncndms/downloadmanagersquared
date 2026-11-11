package engine

import (
	"cmp"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"downloadmanagersquared/engine/internal/archive"
	"downloadmanagersquared/engine/internal/download"
)

// syncSets puts the parts of each split archive in its Set: one starts with
// its first part and stops when its last part is removed.
func (e *Engine) syncSets() {
	e.mu.Lock()
	parallel, saveDir := e.settings.PartsParallel, e.settings.SaveDir
	e.mu.Unlock()
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
		set  string
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
				lone[key] = lonePart{d, part, set}
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
	strays := map[string]map[int]*download.Download{} // in the save folder as files, by set name
	for key, l := range lone {
		// A plain x.rar or x.zip is part of a set only beside its x.r00… or x.z01…
		if m := members[key]; m != nil && m[l.part] == nil {
			m[l.part] = l.d
		} else if m == nil && key == archive.SetKey(filepath.Clean(saveDir), l.set) {
			strays[strings.ToLower(l.set)] = map[int]*download.Download{l.part: l.d}
		}
	}
	for key, m := range members {
		if key == archive.SetKey(filepath.Clean(saveDir), names[key]) {
			strays[strings.ToLower(names[key])] = m
		}
	}
	// A part that came to the save folder as a file while the rest of its
	// archive is a Download as folder elsewhere (added later, named only by its
	// server, or fetched before the switch) takes the set's switch and folder,
	// and is in it from the next look on. Else the set would wait for it for good.
	for key, m := range members {
		stray := strays[strings.ToLower(names[key])]
		if stray == nil || key == archive.SetKey(filepath.Clean(saveDir), names[key]) {
			continue
		}
		into := ""
		for _, d := range m {
			if f := d.State().Folder; f != nil && f.On && f.Into != "" {
				into = f.Into
				break
			}
		}
		taken := slices.ContainsFunc(slices.Collect(maps.Keys(stray)), func(n int) bool { return m[n] != nil })
		if into == "" || taken {
			continue // not a folder, or it has that part: another archive of the same name
		}
		for _, d := range stray {
			if !d.FolderOn() && d.SetFolder(true) == nil {
				d.SetFolderInto(into)
			}
		}
	}
	if e.tails == nil {
		e.tails = map[string]*download.Download{}
	}
	held := map[string]bool{}
	for key, m := range members {
		// Part 1 first: extraction starts from it. A split ZIP's .zip (LastPart) is its last.
		parts := slices.SortedFunc(maps.Keys(m), func(a, b int) int {
			if (a < 1) != (b < 1) {
				return cmp.Compare(b, a)
			}
			return cmp.Compare(a, b)
		})
		ds := make([]*download.Download, len(parts))
		for i, p := range parts {
			ds[i] = m[p]
		}
		var tail *download.Download
		accepted, sequential := false, true
		if s := e.sets[key]; s != nil {
			tail, accepted, sequential = s.Tail(), s.Accepted(), s.Sequential()
		}
		was := e.tails[key]
		e.tails[key] = tail
		if p1 := m[1]; p1 != nil && p1.FolderOn() && sequential && !parallel {
			// Extracted as a folder and read front to back only (a RAR, a solid
			// 7z, or not known yet): a part fetched ahead only waits on disk for
			// the extraction to reach it. So one part at a time, the first not
			// yet fetched, and beside it only the part whose end holds the list
			// of files, while that is still to be read: until it is, no part
			// after the first is of any use. An archive of Entries shorter than
			// a part is extracted wherever its bytes are, so its parts download
			// at once; so do all, where the settings put speed before space.
			front := true
			for i, d := range ds {
				if st := d.Status(); !d.Running() && st != download.StatusQueued {
					continue // fetched, or stopped by the user or an error: the next one's turn
				}
				if front && (accepted || i == 0) || d == tail {
					front = front && d == tail
					continue
				}
				held[d.ID()] = true
				if d.Running() {
					d.Stop(download.StatusQueued)
				}
			}
		}
		if i := slices.Index(ds, tail); tail != nil && i >= 0 {
			// The part whose end holds the list of files goes before part 1,
			// until that is read. Still queued a tick later, no slot was free
			// for it: the last of the parts running gives it its own.
			ds = slices.Insert(slices.Delete(ds, i, i+1), 0, tail)
			if was == tail && tail.Status() == download.StatusQueued {
				for _, d := range slices.Backward(ds) {
					if d.Running() {
						d.Stop(download.StatusQueued)
						break
					}
				}
			}
		}
		ids := make([]string, len(ds))
		for i, d := range ds {
			ids[i] = d.ID()
		}
		e.queue.Order(ids)
	}
	e.queue.Hold(held)
	for key := range e.tails {
		if members[key] == nil {
			delete(e.tails, key)
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
