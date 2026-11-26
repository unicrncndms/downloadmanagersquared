// Package logprivacy is how much engine.log leaves out of what is the user's
// own (the setting Log privacy), and Scrub, which leaves it out: used by
// the log writer, Copy diagnostics and the log calls that word a line Scrub
// can't tell apart.
package logprivacy

import "sync/atomic"

// Level is how much the log leaves out.
type Level int32

const (
	Standard Level = iota // links' paths, secrets, public IPs and file names in paths go; sites stay
	Off                   // lines as written: nothing left out
	Strict                // Standard, and the sites and every file name too
)

var level atomic.Int32

// Set makes l the level, for every line from now on.
func Set(l Level) { level.Store(int32(l)) }

// Get is the level now.
func Get() Level { return Level(level.Load()) }

// Parse is a setting's value as a Level: "off", "strict", else Standard.
func Parse(s string) Level {
	switch s {
	case "off":
		return Off
	case "strict":
		return Strict
	}
	return Standard
}
