package tray

import (
	"image/color"
	"sync"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/engine"
	"downloadmanagersquared/engine/internal/mark"
)

// What the icon says: nothing special, a download running, or something
// (a failed download, a choice to make) that waits for the user.
type iconState int

const (
	iconIdle iconState = iota
	iconBusy
	iconNeedsYou
)

var icons [3]func() []byte

func init() {
	for i := range icons {
		icons[i] = sync.OnceValue(func() []byte { return buildIcon(iconState(i)) })
	}
}

// Icon is an ICO of 16 to 48 px PNGs: the DMS² mark, two chevrons on a tile.
func Icon() []byte { return icons[iconIdle]() }

// iconFor is the icon state of s: needs-you wins over busy, and a paused or
// idle engine is plain.
func iconFor(s engine.StateEvent) iconState {
	busy := s.Active > 0
	for _, d := range s.Downloads {
		if d.Status == download.StatusFailed || d.Status == download.StatusChoosing {
			return iconNeedsYou
		}
	}
	if busy {
		return iconBusy
	}
	return iconIdle
}

// buildIcon is the plain mark with, for the other states, a ringed dot at the
// bottom right: green for downloading, orange for
// needs you.
func buildIcon(st iconState) []byte {
	var dot *color.NRGBA
	switch st {
	case iconBusy:
		dot = &color.NRGBA{0x16, 0xa3, 0x4a, 0xff}
	case iconNeedsYou:
		dot = &color.NRGBA{0xf9, 0x73, 0x16, 0xff}
	}
	var es []mark.Entry
	for _, n := range []int{16, 20, 24, 32, 40, 48} { // Windows picks the one for the DPI
		es = append(es, mark.Entry{N: n, PNG: mark.PNG(mark.Draw(n, dot))})
	}
	return mark.ICO(es...)
}
