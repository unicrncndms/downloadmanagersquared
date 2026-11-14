package window

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// placement is where the window was left: its normal (not maximised)
// rectangle in screen pixels, and whether it was maximised.
type placement struct {
	X, Y, W, H int
	Max        bool
}

const placementFile = "window.json"

func loadPlacement(dir string) (p placement, ok bool) {
	b, err := os.ReadFile(filepath.Join(dir, placementFile))
	return p, err == nil && json.Unmarshal(b, &p) == nil
}

func savePlacement(dir string, p placement) {
	if b, err := json.Marshal(p); err == nil {
		os.WriteFile(filepath.Join(dir, placementFile), b, 0o600)
	}
}

// usable: p is at least minW×minH and a good grab of it (100×40 px) is on one
// of the work areas, so a window left on a monitor that is gone isn't restored
// where it can't be reached.
func (p placement) usable(work []rect, minW, minH int) bool {
	if p.W < minW || p.H < minH {
		return false
	}
	for _, w := range work {
		ox := min(p.X+p.W, int(w.right)) - max(p.X, int(w.left))
		oy := min(p.Y+p.H, int(w.bottom)) - max(p.Y, int(w.top))
		if ox >= 100 && oy >= 40 {
			return true
		}
	}
	return false
}
