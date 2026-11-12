package download

import (
	"reflect"
	"testing"
)

func TestGroupsSummarizeAndOrder(t *testing.T) {
	part := func(id, name string, st Status, size, done int64, set *SetSummary) Summary {
		return Present(Summary{ID: id, Filename: name, Status: st, Size: size, Done: done, Group: "parts:x", GroupName: "x",
			Folder: &FolderSummary{On: true, Into: "out", Set: set}})
	}
	t.Run("order and numbers", func(t *testing.T) {
		running := part("p10", "x.part10.rar", StatusRunning, 100, 50, &SetSummary{Parts: 12, Total: 1200})
		running.Speed = 5
		sums := []Summary{
			part("p2", "x.part2.rar", StatusCompleted, 100, 100, &SetSummary{Parts: 12, Total: 1200}),
			running,
			part("p3", "x.part3.rar", StatusQueued, -1, 0, &SetSummary{Parts: 12, Total: 1200}),
			part("p9", "x.part9.rar", StatusRunning, 100, 0, &SetSummary{Parts: 12, Total: 1200}),
			{ID: "alone", Group: "solo", Filename: "a"},
		}
		gs := Groups(sums)
		if len(gs) != 1 {
			t.Fatalf("%d groups, want 1 (a group of one is a plain row)", len(gs))
		}
		g := gs[0]
		if want := []string{"p9", "p10", "p3", "p2"}; !reflect.DeepEqual(g.Order, want) {
			t.Errorf("order %v, want %v", g.Order, want)
		}
		if !g.Parts || g.Count != 12 || g.Items != 4 || g.Done != 1 || g.Size != 1200 || g.Got != 150 || g.Speed != 5 {
			t.Errorf("got %+v", g)
		}
		if g.Label != "Downloading" || g.Finished {
			t.Errorf("label %q finished %v", g.Label, g.Finished)
		}
		if !g.Pause || g.Resume || !g.PartialFile {
			t.Errorf("buttons %+v", g)
		}
	})
	t.Run("parts listed beyond those laid out, and the extraction's time left", func(t *testing.T) {
		// RAR volumes are laid out as each is probed: 6 known, 7 listed.
		set := &SetSummary{Parts: 6, Total: 700, ETA: 30}
		var sums []Summary
		for i := 1; i <= 7; i++ {
			s := part(string(rune('0'+i)), "x.part"+string(rune('0'+i))+".rar", StatusQueued, 100, 0, set)
			sums = append(sums, s)
		}
		sums[0].Status, sums[0].Speed, sums[0].Done = StatusRunning, 10, 50
		g := Groups(sums)[0]
		if g.Count != 7 {
			t.Errorf("count %d, want the 7 listed", g.Count)
		}
		// 650 bytes left at 10 B/s: the extraction can't end in 30 s.
		if g.ETA != 65 || g.ExtractETA != 65 {
			t.Errorf("eta %v, extraction eta %v, want 65 and 65", g.ETA, g.ExtractETA)
		}
	})
	t.Run("needs you first", func(t *testing.T) {
		a := part("a", "x.part1.rar", StatusRunning, 10, 0, &SetSummary{})
		b := part("b", "x.part2.rar", StatusFailed, 10, 0, &SetSummary{})
		g := Groups([]Summary{a, b})[0]
		if g.Order[0] != "b" || g.Label != "Failed" || g.Tone != "bad" {
			t.Errorf("order %v label %q tone %q", g.Order, g.Label, g.Tone)
		}
	})
	t.Run("fetched, then extracting or waiting or completed", func(t *testing.T) {
		for _, c := range []struct {
			set         SetSummary
			label, tone string
			finished    bool
		}{
			{SetSummary{}, "Extracting", "", false},
			{SetSummary{Note: "Part 3 missing", Files: 1, Of: 4}, "Waiting", "warn", false},
			{SetSummary{Finished: true}, "Completed", "ok", true},
		} {
			set := c.set
			g := Groups([]Summary{part("a", "x.part1.rar", StatusCompleted, 1, 1, &set), part("b", "x.part2.rar", StatusCompleted, 1, 1, &set)})[0]
			if g.Label != c.label || g.Tone != c.tone || g.Finished != c.finished {
				t.Errorf("set %+v: got %q %q %v", c.set, g.Label, g.Tone, g.Finished)
			}
			if c.set.Of > 0 && (g.Files != 1 || g.Of != 4) {
				t.Errorf("extraction count %d of %d", g.Files, g.Of)
			}
		}
	})
	t.Run("a Metalink's size", func(t *testing.T) {
		m := func(id string, size int64) Summary {
			return Present(Summary{ID: id, Filename: id, Status: StatusRunning, Size: size, Group: "ml", GroupName: "Pack"})
		}
		if g := Groups([]Summary{m("a", 3), m("b", 4)})[0]; g.Size != 7 || g.Name != "Pack" || g.Parts || g.Count != 2 {
			t.Errorf("got %+v", g)
		}
		if g := Groups([]Summary{m("a", 3), m("b", 0)})[0]; g.Size != -1 {
			t.Errorf("size %d, want -1", g.Size)
		}
	})
}
