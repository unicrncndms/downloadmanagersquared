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

func TestAllSlowTogetherIsTheServer(t *testing.T) {
	if !allWeak([]linkNow{{"a", 1, 8}, {"b", 2, 5}}) {
		t.Fatal("two slow together: the server")
	}
	if allWeak([]linkNow{{"a", 1, 8}, {"b", 5, 5}}) || allWeak([]linkNow{{"a", 1, 8}}) {
		t.Fatal("one weak connection, or only one, is not the server")
	}
}
