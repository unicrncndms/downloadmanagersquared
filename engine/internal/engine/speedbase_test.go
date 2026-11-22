package engine

import (
	"math"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/download"
)

func TestSpeedBaseIsATimeWeightedAverage(t *testing.T) {
	var b speedBase
	b.add(10e6, 100)
	if b.known() {
		t.Fatal("known after 100 s")
	}
	b.add(4e6, 50)
	if want := (10e6*100 + 4e6*50) / 150; math.Abs(b.Avg-want) > 1 || !b.known() {
		t.Fatalf("avg %v known %v, want %v", b.Avg, b.known(), want)
	}
}

func runningOn(id, limitedBy string, speed float64) download.Summary {
	return download.Summary{ID: "d" + limitedBy, Status: download.StatusRunning, LimitedBy: limitedBy,
		Links: map[string]download.LinkSummary{id: {Speed: speed, Conns: 4}}}
}

func TestOnlyUnheldSamplesCountIntoAnInterfacesAverage(t *testing.T) {
	dir := t.TempDir()
	e := &Engine{ifaces: map[string]*ifaceEntry{"eth": {}, "wifi": {}}, bases: openSpeedBases(dir)}
	speeds := map[string]float64{"eth": 8e6, "wifi": 3e6}
	tick := func(sums ...download.Summary) {
		e.learnSpeedsLocked(sums, speeds, map[string][]string{}, time.Second)
	}
	for range 10 {
		tick(runningOn("eth", "", 8e6), runningOn("wifi", "Wi-Fi's limit", 3e6))
	}
	if b := e.bases.m["eth"]; b == nil || b.Secs != 6 { // 4 s of warm-up not counted
		t.Fatalf("eth %+v, want 6 s counted", b)
	}
	if e.bases.m["wifi"] != nil {
		t.Fatal("a limited connection counted")
	}
	// Two downloads on one connection share it: no sample.
	two := runningOn("eth", "", 4e6)
	two.ID = "other"
	tick(runningOn("eth", "", 4e6), two)
	if e.bases.m["eth"].Secs != 6 {
		t.Fatal("a shared connection counted")
	}
	for range 200 {
		tick(runningOn("eth", "", 8e6))
	}
	if got := e.bases.avg("eth"); math.Abs(got-8e6) > 1 {
		t.Fatalf("avg %v, want 8e6", got)
	}
	e.bases.saved = time.Time{}
	tick(runningOn("eth", "", 8e6))
	if got := openSpeedBases(dir).avg("eth"); math.Abs(got-8e6) > 1 {
		t.Fatalf("after a restart avg %v, want 8e6", got)
	}
}

func TestSlowStateHasNoFlicker(t *testing.T) {
	links := func(speed float64) []linkNow { return []linkNow{{"a", speed, 10}} }
	s := slowState(0, links(4), 10) // under half: counts
	s = slowState(s, links(6), 5)   // between half and 0.7: kept
	if s != 10 {
		t.Fatalf("%v, want 10", s)
	}
	if slowState(s, links(7), 1) != 0 {
		t.Fatal("back over 0.7 of the average resets")
	}
	if slowState(5, nil, 1) != 0 {
		t.Fatal("no known average: never slow")
	}
}

func TestSlowReasons(t *testing.T) {
	name := func(id string) string { return "N" + id }
	both := []linkNow{{"a", 1, 8}, {"b", 1, 8}}
	one := []linkNow{{"a", 1, 8}, {"b", 7, 8}}
	for _, c := range []struct {
		links           []linkNow
		throttled, disk bool
		shared          []string
		want            string
		weak            int
	}{
		{both, true, false, nil, "Slower than usual: the server limits how many requests it takes at once", 2},
		{both, false, true, nil, "Slower than usual: the disk can't keep up", 2},
		{both, false, false, []string{"Wi-Fi", "Ethernet"}, "Slower than usual: Wi-Fi and Ethernet share one line", 2},
		{both, false, false, nil, "Slower than usual: the server sends no faster", 2},
		{one, false, false, nil, "Slower than usual: one connection is slow: Na", 1},
		{one[:1], false, false, nil, "Slower than usual: the server or this connection is slow", 1},
	} {
		if why, weak := slowReason(c.links, c.throttled, c.disk, c.shared, name); why != c.want || len(weak) != c.weak {
			t.Errorf("%q (%d weak), want %q (%d)", why, len(weak), c.want, c.weak)
		}
	}
}

func TestAllSlowTogetherIsTheServer(t *testing.T) {
	if !allWeak([]linkNow{{"a", 1, 8}, {"b", 2, 5}}) {
		t.Fatal("two slow together: the server")
	}
	if allWeak([]linkNow{{"a", 1, 8}, {"b", 5, 5}}) || allWeak([]linkNow{{"a", 1, 8}}) {
		t.Fatal("one weak connection, or only one, is not the server")
	}
}
