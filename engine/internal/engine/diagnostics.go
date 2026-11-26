package engine

import (
	"bufio"
	"cmp"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/logprivacy"
)

// How much of a download's history the report carries.
const (
	diagEvents   = 30
	diagLogLines = 60
)

// Diagnostics is the plain-text report "Copy diagnostics" puts on the
// clipboard for one download: the version, its settings, its connections and
// their states, the last activity-log events and the engine.log lines about
// it. Nothing leaves the PC unless the user pastes it (PRIVACY.md), and even
// then it carries no secret: no cookies or referers, URLs without a password
// or query string, the same for every URL in the log lines. Beyond that it
// leaves out what the setting Log privacy says, as engine.log does: at
// Standard the file's name (its type stays) and the links' paths, at
// Increased the sites too.
func (e *Engine) Diagnostics(id string) (string, error) {
	j, err := e.queue.Get(id)
	if err != nil {
		return "", err
	}
	s := download.Present(j.Summary())
	e.mu.Lock()
	names := map[string]string{}
	for ifID, is := range e.settings.Interfaces {
		names[ifID] = cmp.Or(is.Label, is.Name)
	}
	e.mu.Unlock()

	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	line("DMS² %s diagnostics, %s", Version, time.Now().Format("2006-01-02 15:04:05 -0700"))
	line("")
	level := logprivacy.Get()
	name := s.Filename
	if level != logprivacy.Off && name != "" {
		name = "x" + logprivacy.FileType(name) // the rest of the report is scrubbed at the end
	}
	line("Download %s: %s", s.ID, name)
	line("URL: %s", redactURL(s.URL))
	if s.PageURL != "" {
		line("Landing page: %s", redactURL(s.PageURL))
	}
	line("Status: %s (%s)%s", s.Label, s.Status, prefixed(" · ", s.Note))
	if s.Error != "" {
		line("Why: %s", s.Error)
	}
	line("Progress: %d of %d bytes, %.0f B/s, ranges %v, VPN %s", s.Done, s.Size, s.Speed, s.RangeOK, cmp.Or(string(s.VPN), "default"))
	if s.Limit > 0 {
		line("Speed cap: %d B/s", s.Limit)
	}
	if s.LimitedBy != "" {
		line("Held back by: %s (%d B/s)", s.LimitedBy, s.LimitedTo)
	}
	if f := s.Folder; f != nil {
		line("Download as folder: on %v, %d of %d files, keep %s/%s, extraction failed %v%s", f.On, f.Files, f.Of, f.Keep, f.Keeping, f.ExtractFailed, prefixed(" · ", f.Note))
		if f.Set != nil {
			line("Split archive: %d parts, missing %v, failing %v%s", f.Set.Parts, f.Set.Missing, f.Set.Failing, prefixed(" · ", f.Set.Note))
		}
	}
	line("")
	line("Connections:")
	ids := make([]string, 0, len(s.Links))
	for ifID := range s.Links {
		ids = append(ids, ifID)
	}
	slices.Sort(ids)
	for _, ifID := range ids {
		l := s.Links[ifID]
		line("- %s (%s): %s [%s], %d requests, %.0f B/s, %d bytes%s%s", cmp.Or(names[ifID], ifID), ifID, l.Label, l.Status, l.Conns, l.Speed, l.Bytes,
			prefixed(", mirror ", l.Mirror), prefixed(" · ", l.Error))
	}
	if len(ids) == 0 {
		line("- none yet")
	}

	if d, ok := j.(*download.Download); ok {
		events := d.Log()
		if len(events) > diagEvents {
			events = events[len(events)-diagEvents:]
		}
		line("")
		line("Activity (last %d):", len(events))
		for _, ev := range events {
			line("%s %s%s", ev.Time.Local().Format("2006-01-02 15:04:05"), prefixed("", cmp.Or(names[ev.Iface], ev.Iface)+" · "), redactText(ev.Text))
		}
	}

	line("")
	logs := e.logLinesAbout(id)
	line("engine.log (last %d lines about it):", len(logs))
	for _, l := range logs {
		line("%s", redactText(l))
	}
	// As the setting Log privacy says, as engine.log; Off: as before it, without secrets.
	return logprivacy.Scrub(b.String(), e.dir, level), nil
}

// logLinesAbout is the last lines of engine.log (and the one before it) that
// name the download.
func (e *Engine) logLinesAbout(id string) []string {
	var out []string
	for _, name := range []string{"engine.log.old", "engine.log"} {
		f, err := os.Open(filepath.Join(e.dir, name))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			if t := sc.Text(); strings.Contains(t, id) {
				out = append(out, t)
			}
		}
		f.Close()
	}
	if len(out) > diagLogLines {
		out = out[len(out)-diagLogLines:]
	}
	return out
}

// redactURL keeps what tells where a URL goes and drops what may be a secret:
// the password, the query string and the fragment.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		raw, _, _ = strings.Cut(raw, "?")
		raw, _, _ = strings.Cut(raw, "#")
		return secretInText.ReplaceAllString(raw, "$1$2…")
	}
	if u.User != nil {
		u.User = url.User(u.User.Username())
	}
	q := u.RawQuery != ""
	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
	s := u.String()
	if q {
		s += "?…"
	}
	return s
}

var urlInText = regexp.MustCompile(`(?i)\b(?:https?|ftps?|sftp)://[^\s"'<>]+`)

// secretInText is a key, token, password, cookie or signature written out as
// name=value or name: value.
var secretInText = regexp.MustCompile(`(?i)\b(cookie|set-cookie|authorization|password|passwd|pass|token|access_token|api[_-]?key|key|secret|sig|signature|session|sid|auth)(\s*[=:]\s*)[^\s,;&"']+`)

// redactText is redactURL for every URL in a line, and hides any value that
// looks like a secret.
func redactText(s string) string {
	s = urlInText.ReplaceAllStringFunc(s, redactURL)
	return secretInText.ReplaceAllString(s, "$1$2…")
}

func prefixed(prefix, s string) string {
	if s == "" || s == " · " {
		return ""
	}
	return prefix + s
}
