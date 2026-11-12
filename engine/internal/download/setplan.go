package download

import (
	"cmp"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"downloadmanagersquared/engine/internal/archive"
)

// SetsPlan is what the engine carries out each tick for the Sets of split
// archives, decided by PlanSets from the queue as it is.
type SetsPlan struct {
	Members map[string]map[int]*Download // each Set's parts, by Set key and part number
	Names   map[string]string            // each Set's archive name
	Adopt   []Adoption                   // stray parts to put in their Set's folder
	Stop    []*Download                  // running parts to stop as queued
	Hold    map[string]bool              // ids of parts the queue starts no more of
	Order   map[string][]string          // each Set's part ids in the order they queue
	Tails   map[string]string            // the part id each Set had start first, to pass back next tick
}

// Adoption is a part saved as a plain file that takes its Set's folder.
type Adoption struct {
	D    *Download
	Into string
}

// PlanSets decides how the Sets of the queue's downloads stand: who is in
// which, what to hold back, stop and put first. sets are the Sets now running,
// by key; lastTails is Tails of the tick before.
func PlanSets(ds []*Download, sets map[string]*Set, parallel bool, saveDir string, lastTails map[string]string) SetsPlan {
	byID := map[string]*Download{}
	var facts []setFact
	for _, d := range ds {
		key, set, part, lone := setOf(d)
		if key == "" {
			continue
		}
		f := setFact{id: d.ID(), key: key, set: set, part: part, lone: lone, status: d.Status(), running: d.Running(), folderOn: d.FolderOn()}
		if fs := d.State().Folder; fs != nil && fs.On && fs.Into != "" {
			f.folderInto = fs.Into
		}
		facts = append(facts, f)
		byID[f.id] = d
	}
	states := map[string]setState{}
	for key, s := range sets {
		st := setState{accepted: s.accepted.Load(), sequential: s.inOrder.Load()}
		if t := s.tail.Load(); t != nil {
			st.tail = t.ID()
		}
		states[key] = st
	}
	p := planSets(facts, states, parallel, saveDir, lastTails)
	out := SetsPlan{
		Members: map[string]map[int]*Download{}, Names: p.names, Hold: p.hold, Order: p.order, Tails: p.tails,
	}
	for key, m := range p.members {
		out.Members[key] = map[int]*Download{}
		for part, id := range m {
			out.Members[key][part] = byID[id]
		}
	}
	for _, a := range p.adopt {
		out.Adopt = append(out.Adopt, Adoption{byID[a.id], a.into})
	}
	for _, id := range p.stop {
		out.Stop = append(out.Stop, byID[id])
	}
	return out
}

// setFact is what a download looks like to planSets.
type setFact struct {
	id         string
	key, set   string // its Set's key and archive name
	part       int
	lone       bool // a plain x.rar or x.zip, see setOf
	status     Status
	running    bool
	folderOn   bool   // Download as folder is on
	folderInto string // the folder it extracts into, when that is on
}

// setState is what a running Set says of itself.
type setState struct {
	tail       string // id of the part whose end it needs, "" for none
	accepted   bool
	sequential bool
}

type adopt struct{ id, into string }

type setPlanned struct {
	members map[string]map[int]string
	names   map[string]string
	adopt   []adopt
	stop    []string
	hold    map[string]bool
	order   map[string][]string
	tails   map[string]string
}

// planSets is PlanSets over plain values: facts in queue order, the running
// Sets' states by key, and the tails of the tick before.
func planSets(facts []setFact, states map[string]setState, parallel bool, saveDir string, lastTails map[string]string) setPlanned {
	p := setPlanned{
		members: map[string]map[int]string{}, names: map[string]string{},
		hold: map[string]bool{}, order: map[string][]string{}, tails: map[string]string{},
	}
	members, names := p.members, p.names
	byID := map[string]setFact{}
	lone := map[string]setFact{}
	for _, f := range facts {
		byID[f.id] = f
		if f.lone {
			if _, ok := lone[f.key]; !ok {
				lone[f.key] = f
			}
			continue
		}
		if members[f.key] == nil {
			members[f.key], names[f.key] = map[int]string{}, f.set
		}
		if members[f.key][f.part] == "" {
			members[f.key][f.part] = f.id
		}
	}
	saveKey := func(set string) string { return archive.SetKey(filepath.Clean(saveDir), set) }
	strays := map[string]map[int]string{} // in the save folder as files, by set name
	for key, l := range lone {
		// A plain x.rar or x.zip is part of a set only beside its x.r00… or x.z01…
		if m := members[key]; m != nil && m[l.part] == "" {
			m[l.part] = l.id
		} else if m == nil && key == saveKey(l.set) {
			strays[strings.ToLower(l.set)] = map[int]string{l.part: l.id}
		}
	}
	for key, m := range members {
		if key == saveKey(names[key]) {
			strays[strings.ToLower(names[key])] = m
		}
	}
	// A part that came to the save folder as a file while the rest of its
	// archive is a Download as folder elsewhere (added later, named only by its
	// server, or fetched before the switch) takes the set's switch and folder,
	// and is in it from the next look on. Else the set would wait for it for good.
	for key, m := range members {
		stray := strays[strings.ToLower(names[key])]
		if stray == nil || key == saveKey(names[key]) {
			continue
		}
		into := ""
		for _, id := range m {
			if f := byID[id]; f.folderInto != "" {
				into = f.folderInto
				break
			}
		}
		taken := slices.ContainsFunc(slices.Collect(maps.Keys(stray)), func(n int) bool { return m[n] != "" })
		if into == "" || taken {
			continue // not a folder, or it has that part: another archive of the same name
		}
		for _, id := range stray {
			if !byID[id].folderOn {
				p.adopt = append(p.adopt, adopt{id, into})
			}
		}
	}
	stopped := map[string]bool{}
	stop := func(id string) {
		stopped[id] = true
		p.stop = append(p.stop, id)
	}
	running := func(id string) bool { return byID[id].running && !stopped[id] }
	for key, m := range members {
		// Part 1 first: extraction starts from it. A split ZIP's .zip (LastPart) is its last.
		parts := slices.SortedFunc(maps.Keys(m), func(a, b int) int {
			if (a < 1) != (b < 1) {
				return cmp.Compare(b, a)
			}
			return cmp.Compare(a, b)
		})
		ds := make([]string, len(parts))
		for i, part := range parts {
			ds[i] = m[part]
		}
		st, ok := states[key]
		if !ok {
			st.sequential = true
		}
		tail, accepted, sequential := st.tail, st.accepted, st.sequential
		stops := len(p.stop)
		was := lastTails[key]
		p.tails[key] = tail
		if p1, ok := byID[m[1]]; ok && p1.folderOn && sequential && !parallel {
			// Extracted as a folder and read front to back only (a RAR, a solid
			// 7z, or not known yet): a part fetched ahead only waits on disk for
			// the extraction to reach it. So one part at a time, the first not
			// yet fetched, and beside it only the part whose end holds the list
			// of files, while that is still to be read: until it is, no part
			// after the first is of any use. An archive of Entries shorter than
			// a part is extracted wherever its bytes are, so its parts download
			// at once; so do all, where the settings put speed before space.
			front := true
			for i, id := range ds {
				if !running(id) && byID[id].status != StatusQueued {
					continue // fetched, or stopped by the user or an error: the next one's turn
				}
				if front && (accepted || i == 0) || id == tail {
					front = front && id == tail
					continue
				}
				p.hold[id] = true
				if running(id) {
					stop(id)
				}
			}
		}
		if i := slices.Index(ds, tail); tail != "" && i >= 0 {
			// The part whose end holds the list of files goes before part 1,
			// until that is read. Still queued a tick later, no slot was free
			// for it: the last of the parts running gives it its own.
			ds = slices.Insert(slices.Delete(ds, i, i+1), 0, tail)
			if was == tail && byID[tail].status == StatusQueued {
				for _, id := range slices.Backward(ds) {
					if running(id) {
						stop(id)
						break
					}
				}
			}
		}
		// A part earlier in the order still queued a tick later, while a later
		// part of the same Set runs (one added before the others, say, took
		// the only slot): no slot was free for it, so the last running part
		// gives it its own. The Set's parts then fetch in order.
		front := slices.IndexFunc(ds, func(id string) bool {
			return byID[id].status == StatusQueued && !byID[id].running && !p.hold[id] && !stopped[id]
		})
		if front >= 0 && len(p.stop) == stops {
			p.tails[key+waitingKey] = ds[front]
			if lastTails[key+waitingKey] == ds[front] {
				for _, id := range slices.Backward(ds[front+1:]) {
					if running(id) {
						stop(id)
						break
					}
				}
			}
		}
		p.order[key] = ds
	}
	return p
}

// waitingKey marks, among the tails passed back to the next tick, the first
// part each Set had queued: the engine keeps them without reading them.
const waitingKey = "/waiting"
