package download

import (
	"path/filepath"
	"slices"
	"testing"

	"downloadmanagersquared/engine/internal/archive"
)

var planSave = filepath.Clean("/save")

func planKey(dir, set string) string { return archive.SetKey(dir, set) }

// planPart is a fact for part n of x.7z in dir, queued, with its folder on or off.
func planPart(id string, n int, dir string, folder bool) setFact {
	f := setFact{id: id, key: planKey(dir, "x.7z"), set: "x.7z", part: n, status: StatusQueued, folderOn: folder}
	if folder {
		f.folderInto = dir
	}
	return f
}

func TestPlanSetsOrder(t *testing.T) {
	k := planKey("/out", "x.zip")
	zip := func(id string, n int, lone bool) setFact {
		return setFact{id: id, key: k, set: "x.zip", part: n, lone: lone, status: StatusQueued}
	}
	// the .zip arrives first and is lone; it joins only beside z01
	p := planSets([]setFact{zip("zip", archive.LastPart, true), zip("z02", 2, false), zip("z01", 1, false)}, nil, false, planSave, nil)
	if got := p.order[k]; !slices.Equal(got, []string{"z01", "z02", "zip"}) {
		t.Fatalf("order = %v", got)
	}
	// alone, it is no set
	p = planSets([]setFact{zip("zip", archive.LastPart, true)}, nil, false, planSave, nil)
	if len(p.members) != 0 {
		t.Fatalf("lone zip made a set: %v", p.members)
	}
	// a lone x.rar does not displace a part 1 already there
	p = planSets([]setFact{zip("a", 1, false), zip("b", 1, true)}, nil, false, planSave, nil)
	if p.members[k][1] != "a" {
		t.Fatalf("members = %v", p.members)
	}
}

func TestPlanSetsAdopt(t *testing.T) {
	out := filepath.Clean("/out")
	stray := planPart("s", 2, planSave, false)
	for _, tc := range []struct {
		name  string
		facts []setFact
		adopt bool
	}{
		{"folder set takes the stray", []setFact{planPart("a", 1, out, true), stray}, true},
		{"set without a folder", []setFact{planPart("a", 1, out, false), stray}, false},
		{"set has that part already", []setFact{planPart("a", 1, out, true), planPart("b", 2, out, true), stray}, false},
		{"stray already a folder", []setFact{planPart("a", 1, out, true), planPart("s", 2, planSave, true)}, false},
	} {
		p := planSets(tc.facts, nil, false, planSave, nil)
		if got := len(p.adopt) == 1 && p.adopt[0].id == "s" && p.adopt[0].into == out; got != tc.adopt {
			t.Errorf("%s: adopt = %v", tc.name, p.adopt)
		}
	}
}

func TestPlanSetsHold(t *testing.T) {
	k := planKey("/out", "x.7z")
	mk := func(running2 bool) []setFact {
		fs := []setFact{planPart("1", 1, "/out", true), planPart("2", 2, "/out", true), planPart("3", 3, "/out", true), planPart("4", 4, "/out", true)}
		if running2 {
			fs[1].running, fs[1].status = true, StatusRunning
		}
		return fs
	}
	seq := map[string]setState{k: {sequential: true}}
	held := func(p setPlanned) []string {
		return slices.Sorted(func(y func(string) bool) {
			for id := range p.hold {
				y(id)
			}
		})
	}
	// not accepted: only part 1 goes
	if got := held(planSets(mk(false), seq, false, planSave, nil)); !slices.Equal(got, []string{"2", "3", "4"}) {
		t.Errorf("not accepted: held %v", got)
	}
	// accepted: the first not fetched goes, the rest wait
	fs := mk(false)
	fs[0].status = StatusCompleted
	if got := held(planSets(fs, map[string]setState{k: {sequential: true, accepted: true}}, false, planSave, nil)); !slices.Equal(got, []string{"3", "4"}) {
		t.Errorf("accepted: held %v", got)
	}
	// a tail goes beside the front part
	if got := held(planSets(mk(false), map[string]setState{k: {sequential: true, tail: "4"}}, false, planSave, nil)); !slices.Equal(got, []string{"2", "3"}) {
		t.Errorf("tail: held %v", got)
	}
	// a held part running is stopped
	p := planSets(mk(true), seq, false, planSave, nil)
	if !slices.Equal(p.stop, []string{"2"}) {
		t.Errorf("stop = %v", p.stop)
	}
	// parallel, not sequential, or no folder: nothing held
	if p := planSets(mk(true), seq, true, planSave, nil); len(p.hold)+len(p.stop) != 0 {
		t.Errorf("parallel held %v", p.hold)
	}
	if p := planSets(mk(true), map[string]setState{k: {}}, false, planSave, nil); len(p.hold)+len(p.stop) != 0 {
		t.Errorf("not sequential held %v", p.hold)
	}
	nf := mk(true)
	nf[0].folderOn = false
	if p := planSets(nf, seq, false, planSave, nil); len(p.hold)+len(p.stop) != 0 {
		t.Errorf("no folder held %v", p.hold)
	}
}

func TestPlanSetsTail(t *testing.T) {
	k := planKey("/out", "x.7z")
	fs := []setFact{planPart("1", 1, "/out", true), planPart("2", 2, "/out", true), planPart("3", 3, "/out", true)}
	st := map[string]setState{k: {tail: "3"}} // sequential unset: not held
	p := planSets(fs, st, true, planSave, nil)
	if !slices.Equal(p.order[k], []string{"3", "1", "2"}) || p.tails[k] != "3" || len(p.stop) != 0 {
		t.Fatalf("first tick: %v %v %v", p.order[k], p.tails, p.stop)
	}
	// still queued a tick later: the last running part gives up its slot
	fs[0].running, fs[0].status = true, StatusRunning
	fs[1].running, fs[1].status = true, StatusRunning
	p = planSets(fs, st, true, planSave, p.tails)
	if !slices.Equal(p.stop, []string{"2"}) {
		t.Fatalf("second tick stop = %v", p.stop)
	}
	// the tail running: nothing stopped
	fs[2].running, fs[2].status = true, StatusRunning
	if p = planSets(fs, st, true, planSave, p.tails); len(p.stop) != 0 {
		t.Fatalf("tail running stop = %v", p.stop)
	}
}

// A later part that took the only slot before the others were added gives
// it to the first part still queued a tick later, so a Set fetches in order.
func TestPlanSetsALaterPartGivesItsSlotToAnEarlierOne(t *testing.T) {
	fs := []setFact{planPart("5", 5, "/out", true), planPart("1", 1, "/out", true), planPart("2", 2, "/out", true)}
	fs[0].running, fs[0].status = true, StatusRunning
	p := planSets(fs, nil, true, planSave, nil)
	if len(p.stop) != 0 {
		t.Fatalf("first tick stop = %v", p.stop)
	}
	if p = planSets(fs, nil, true, planSave, p.tails); !slices.Equal(p.stop, []string{"5"}) {
		t.Fatalf("second tick stop = %v, want part 5", p.stop)
	}
	// Part 1 running and part 2 queued behind it: nothing later runs, nothing stops.
	fs[0].running, fs[0].status = false, StatusQueued
	fs[1].running, fs[1].status = true, StatusRunning
	p = planSets(fs, nil, true, planSave, nil)
	if p = planSets(fs, nil, true, planSave, p.tails); len(p.stop) != 0 {
		t.Fatalf("in order stop = %v", p.stop)
	}
}
