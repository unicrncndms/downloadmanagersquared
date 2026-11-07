package download

import (
	"regexp"
	"strings"
	"time"
)

// maxEvents is how many events a download keeps; older ones are dropped.
const maxEvents = 50

// Event is one line of a download's activity log: something it or one of its
// interfaces did, as the user saw it in the list.
type Event struct {
	Time  time.Time `json:"time"`
	Iface string    `json:"iface,omitempty"` // the interface it concerns; empty for the download as a whole
	Text  string    `json:"text"`
}

// observeLocked logs what changed since the last summary: the download's
// status, error or checksum result, each link's status, error or backoff,
// and a mirror being dropped. Comparing summaries, rather than logging at
// every place that changes state, keeps the log in the user's words (the
// labels the list shows) and the rest of the package unaware of it.
//
// The first summary after a restart only learns the current state, unless
// the log is empty (a new download), so a restart doesn't log everything
// again.
func (d *Download) observeLocked(s Summary, now time.Time) {
	seen := map[string]string{}
	var events []Event
	note := func(key, value, iface, text string) {
		seen[key] = value
		if d.seen != nil && d.seen[key] != value || d.seen == nil && len(d.st.Log) == 0 {
			events = append(events, Event{Time: now, Iface: iface, Text: scrub(text)})
		}
	}

	note("", string(s.Status)+"\x00"+s.Error+"\x00"+s.hashResult, "", join(s.Label, s.Error, hashText(s)))
	for id, l := range s.Links {
		note("link:"+id, string(l.Status)+"\x00"+l.Error+"\x00"+boolKey(l.busy), id, join(l.Label, l.Error))
	}
	for _, m := range s.Mirrors {
		if m.Dropped != "" {
			note("mirror:"+m.URL, m.Dropped, "", "Mirror "+hostOf(m.URL)+" dropped: "+m.Dropped)
		}
	}
	d.seen = seen
	if len(events) == 0 {
		return
	}
	d.st.Log = append(d.st.Log, events...)
	if n := len(d.st.Log) - maxEvents; n > 0 {
		d.st.Log = append([]Event(nil), d.st.Log[n:]...)
	}
}

// Log is the download's activity, oldest first.
func (d *Download) Log() []Event {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Event{}, d.st.Log...)
}

func hashText(s Summary) string {
	switch s.hashResult {
	case "":
		return ""
	case "match":
		return "checksum matches"
	}
	return "checksum: " + s.hashResult
}

func join(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" && !strings.Contains(strings.Join(out, " "), p) {
			out = append(out, p)
		}
	}
	return strings.Join(out, ": ")
}

func boolKey(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// A link's query string often carries a token; the log never shows it.
var queryRE = regexp.MustCompile(`(?i)\b((?:https?|ftp)://[^\s?#"']+)[?#][^\s"']*`)

func scrub(s string) string { return queryRE.ReplaceAllString(s, "$1") }
