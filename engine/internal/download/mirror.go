package download

import (
	"cmp"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// MaxMirrors caps the mirrors a download keeps: the state goes to the
// browser every second, and more mirrors than interfaces help little.
const MaxMirrors = 16

// Mirror is one source of the file, from a Metalink or a server's
// Link: rel=duplicate. A download with mirrors lists its source URL among them.
type Mirror struct {
	URL     string `json:"url"`
	Dropped string `json:"dropped,omitempty"` // why it's left out: it serves another file, or refuses it
}

// mirrorLocked is the mirror at u, or nil when u isn't one: the download has
// no mirrors, or u is a link the browser fetched.
func (d *Download) mirrorLocked(u string) *Mirror {
	for i := range d.st.Mirrors {
		if d.st.Mirrors[i].URL == u {
			return &d.st.Mirrors[i]
		}
	}
	return nil
}

// sourceLocked is the URL a new link of the interface starts from. The main
// interface takes the source URL while it's still in; the others the mirror
// the fewest other interfaces use, in the mirrors' order. "" when every
// mirror is dropped.
func (d *Download) sourceLocked(ifaceID string, main bool) string {
	if len(d.st.Mirrors) == 0 {
		return d.st.URL
	}
	if m := d.mirrorLocked(d.st.URL); main && (m == nil || m.Dropped == "") {
		return d.st.URL
	}
	use := map[string]int{}
	for id, l := range d.st.Links {
		if id != ifaceID && !l.stuck() {
			use[l.URL]++
		}
	}
	best := ""
	for _, m := range d.st.Mirrors {
		if m.Dropped == "" && (best == "" || use[m.URL] < use[best]) {
			best = m.URL
		}
	}
	return best
}

// newLinkLocked is an interface's first link. Only the source URL gets the
// cookie and referer: they were the browser's for that site.
func (d *Download) newLinkLocked(ifaceID string, main bool) *Link {
	l := &Link{Status: LinkPending}
	l.moveTo(cmp.Or(d.sourceLocked(ifaceID, main), d.st.URL), d.st)
	return l
}

// leaveMirrorLocked: a link stuck on a mirror drops the mirror and moves to
// the next one, so the interface goes on.
func (d *Download) leaveMirrorLocked(ifaceID string, l *Link) {
	m := d.mirrorLocked(l.URL)
	if m == nil || !l.stuck() {
		return
	}
	m.Dropped = cmp.Or(l.Error, "left out")
	if u := d.sourceLocked(ifaceID, false); u != "" {
		l.moveTo(u, d.st)
	}
}

// addMirrorsLocked takes the mirrors the source URL's server named, once its
// answer decided the file. Interfaces that haven't got the file yet spread over them.
func (d *Download) addMirrorsLocked(decider string, dups []string) {
	d.st.Mirrors = []Mirror{{URL: d.st.URL}}
	for _, u := range dups {
		if len(d.st.Mirrors) < MaxMirrors && d.mirrorLocked(u) == nil {
			d.st.Mirrors = append(d.st.Mirrors, Mirror{URL: u})
		}
	}
	for id, l := range d.st.Links {
		if id != decider && l.URL == d.st.URL && l.Status != LinkReady && l.Status != LinkResolving {
			l.moveTo(d.sourceLocked(id, false), d.st)
		}
	}
}

// mirrorMismatchLocked is why an answer from a mirror isn't the file this
// download wants, or "". Mirrors are told apart by size and checksum, not by
// ETag or date: each server makes up its own.
func (d *Download) mirrorMismatchLocked(r probeResult) string {
	if d.st.MetaSize > 0 && r.size != d.st.MetaSize {
		return fmt.Sprintf("different file size (%d vs %d bytes)", r.size, d.st.MetaSize)
	}
	sha, md := cmp.Or(d.st.SHA256, d.st.Version.SHA256), cmp.Or(d.st.MD5, d.st.Version.MD5)
	if r.version.SHA256 != "" && sha != "" && r.version.SHA256 != sha ||
		r.version.MD5 != "" && md != "" && r.version.MD5 != md {
		return "a different file: its checksum doesn't match"
	}
	return ""
}

// duplicates are the mirrors a Link header names (RFC 6249's rel=duplicate),
// lowest pri first, resolved against base. Only http and https.
func duplicates(links []string, base *url.URL) []string {
	type dup struct {
		u   string
		pri int
	}
	var ds []dup
	for _, h := range links {
		for h != "" {
			start := strings.IndexByte(h, '<')
			end := strings.IndexByte(h, '>')
			if start < 0 || end < start {
				break
			}
			target := h[start+1 : end]
			h = h[end+1:]
			params := h
			if i := strings.IndexByte(h, '<'); i >= 0 {
				params, h = h[:i], h[i:]
			} else {
				h = ""
			}
			isDup, pri := false, 999999
			for p := range strings.SplitSeq(params, ";") {
				k, v, _ := strings.Cut(strings.TrimSpace(strings.TrimRight(strings.TrimSpace(p), ",")), "=")
				v = strings.Trim(strings.TrimSpace(v), `"`)
				switch strings.ToLower(strings.TrimSpace(k)) {
				case "rel":
					for r := range strings.FieldsSeq(strings.ToLower(v)) {
						isDup = isDup || r == "duplicate"
					}
				case "pri":
					fmt.Sscan(v, &pri)
				}
			}
			if !isDup {
				continue
			}
			u, err := url.Parse(target)
			if err != nil {
				continue
			}
			if base != nil {
				u = base.ResolveReference(u)
			}
			if u.Scheme == "http" || u.Scheme == "https" {
				ds = append(ds, dup{u.String(), pri})
			}
		}
	}
	slices.SortStableFunc(ds, func(a, b dup) int { return cmp.Compare(a.pri, b.pri) }) // equal priorities keep the server's order
	out := make([]string, len(ds))
	for i, x := range ds {
		out[i] = x.u
	}
	return out
}

func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil {
		return p.Host
	}
	return ""
}
