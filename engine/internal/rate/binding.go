package rate

// Cap is a speed limit that may hold a download back: its name for the user,
// the limit in bytes per second (0 = none) and the speed passing through it now.
type Cap struct {
	Name  string
	Limit int64
	Speed float64
}

// Binding is the first of caps (listed most specific first) whose speed is
// within 15% of its limit: the one holding the download back. ok is false when
// none is, so a download that is slow for another reason is not blamed on a limit.
func Binding(caps ...Cap) (c Cap, ok bool) {
	for _, c := range caps {
		if c.Limit > 0 && c.Speed >= 0.85*float64(c.Limit) {
			return c, true
		}
	}
	return Cap{}, false
}
