package window

import (
	"testing"
)

func TestPlacementMustBeReachable(t *testing.T) {
	work := []rect{{0, 0, 1920, 1040}}
	for _, c := range []struct {
		p    placement
		want bool
	}{
		{placement{100, 100, 900, 720, false}, true},
		{placement{-300, 50, 900, 720, true}, true},    // partly off the left edge, still reachable
		{placement{2500, 100, 900, 720, false}, false}, // on a monitor that is gone
		{placement{100, 100, 200, 200, false}, false},  // smaller than the minimum
		{placement{1900, 100, 900, 720, false}, false}, // only 20 px showing
	} {
		if got := c.p.usable(work, 420, 360); got != c.want {
			t.Errorf("%+v usable = %v, want %v", c.p, got, c.want)
		}
	}
}

func TestPlacementIsKeptInTheDataFolder(t *testing.T) {
	dir := t.TempDir()
	if _, ok := loadPlacement(dir); ok {
		t.Fatal("found a placement in an empty folder")
	}
	want := placement{10, 20, 800, 600, true}
	savePlacement(dir, want)
	if got, ok := loadPlacement(dir); !ok || got != want {
		t.Fatalf("loaded %+v %v, want %+v", got, ok, want)
	}
}
