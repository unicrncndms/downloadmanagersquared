package logprivacy

import (
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Scrub takes out of a log line what is the user's own, so engine.log can be
// sent as it is: a URL keeps its scheme and host only, a value that looks
// like a secret goes, a public IP address goes (private, VPN and link-local
// ones stay: they tell the lines apart), and a path keeps its drive and its
// file type only, except under the engine's data folder (data), which is
// written as %DATA%; a name inside an archive's folders (big/file001.bin)
// keeps its file type only too. File names the engine logs alone (a Set's
// name, an archive's Entries) are kept out at the log calls.
// That is the setting Log privacy at Standard. Strict also leaves out the
// sites, a URL keeping its scheme only, and every name that has a file type
// or is a host (x.rar, x.com), but the engine's own files. Off leaves the
// line as it is. At every level DMS²'s own code stays as it is: its pages'
// addresses (https://dms.invalid/ui/card.js:120 in a script error) and its
// Go source files (set.go:412 in a crash), which say nothing of the user's.
func Scrub(s, data string, level Level) string {
	if level == Off {
		return s
	}
	strict := level == Strict
	// The pages' addresses are set aside first, so nothing below touches them.
	own := ownPageRE.FindAllString(s, -1)
	for i, o := range own {
		s = strings.Replace(s, o, held(i), 1)
	}
	s = urlRE.ReplaceAllStringFunc(s, func(m string) string { return scrubURL(m, strict) })
	s = secretRE.ReplaceAllString(s, "$1$2…")
	if data != "" {
		s = replaceFold(s, data+`\`, `%DATA%\`)
	}
	s = pathRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := pathRE.FindStringSubmatch(m)
		return scrubPaths(sub[1]) + sub[2]
	})
	s = relRE.ReplaceAllStringFunc(s, func(m string) string {
		if isSource(m) {
			return m
		}
		return "…/x" + FileType(m[strings.LastIndex(m, "/")+1:])
	})
	if strict {
		s = scrubNames(s)
	}
	s = ip4RE.ReplaceAllStringFunc(s, func(m string) string { return scrubIP(m, "<public IPv4>") })
	s = ip6RE.ReplaceAllStringFunc(s, func(m string) string { return scrubIP(m, "<public IPv6>") })
	for i, o := range own {
		s = strings.Replace(s, held(i), o, 1)
	}
	return s
}

// ownPageRE is an address of DMS²'s own pages (internal/window's origin).
var ownPageRE = regexp.MustCompile(`https://dms\.invalid/[^\s"'<>]*`)

// held stands in for the ith own address while Scrub runs: no rule matches it.
func held(i int) string { return "\x00" + strconv.Itoa(i) + "\x00" }

// isSource is a Go source file of DMS² or a library it uses.
func isSource(name string) bool { return strings.HasSuffix(name, ".go") }

// replaceFold is s with every old, whatever its case (Windows paths), as new.
func replaceFold(s, old, new string) string {
	lower, lold := strings.ToLower(s), strings.ToLower(old)
	if len(lower) != len(s) { // a letter whose lower case is longer: leave it
		return strings.ReplaceAll(s, old, new)
	}
	var b strings.Builder
	for {
		i := strings.Index(lower, lold)
		if i < 0 {
			return b.String() + s
		}
		b.WriteString(s[:i] + new)
		s, lower = s[i+len(old):], lower[i+len(old):]
	}
}

// scrubNames is every name with a file type or a host's ending as x and that
// ending (x.part7.rar, x.com), but the engine's own files under %DATA%.
func scrubNames(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range nameRE.FindAllStringIndex(s, -1) {
		name := s[m[0]:m[1]]
		if strings.HasSuffix(s[:m[0]], `%DATA%\`) || ownFiles[strings.ToLower(name)] || isSource(name) {
			continue
		}
		ext := FileType(name)
		if ext == "" {
			ext = name[strings.LastIndex(name, "."):]
		}
		b.WriteString(s[last:m[0]] + "x" + ext)
		last = m[1]
	}
	return b.String() + s[last:]
}

// ownFiles are the engine's files, which say nothing of the user's.
var ownFiles = map[string]bool{"engine.log": true, "crash.log": true, "settings.json": true, "recipes.json": true, "stepbacks.json": true, "speeds.json": true, "sftp.json": true}

var (
	urlRE    = regexp.MustCompile(`(?i)\b(?:https?|ftps?|sftp|wss?|magnet):(?://)?[^\s"'<>]+`)
	secretRE = regexp.MustCompile(`(?i)\b(cookie|set-cookie|authorization|password|passwd|pass|token|access_token|api[_-]?key|key|secret|sig|signature|session|sid|auth)(\s*[=:]\s*)[^\s,;&"']+`)
	// A path runs to the ": " an error puts after it, a quote or the line's
	// end, so a name with spaces goes whole; rename's two paths go together.
	// On a drive (C:\) or a network share (\\server\share).
	pathRE = regexp.MustCompile(`((?:[A-Za-z]:\\|\\\\[^\\\s"]+\\)[^"\n]*?)(: |"|\n|$)`)
	// A name in an archive's folders, as an error writes it: big/file001.bin.
	relRE = regexp.MustCompile(`[^\s/\\:"'<>()]+(?:/[^\s/\\:"'<>()]+)*/[^\s/\\:"'<>()]*\.[A-Za-z][A-Za-z0-9]{0,4}\b`)
	// A name with a file type, or a host: the last ending starts with a letter (not 1.0.0 or an address).
	nameRE = regexp.MustCompile(`(?i)\b[\w-]+(?:\.[\w-]+)*\.[a-z][a-z0-9]{0,4}\b`)
	ip4RE  = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	ip6RE  = regexp.MustCompile(`(?i)[0-9a-f]{0,4}(?::[0-9a-f]{0,4}){2,7}`)
	// The file type kept of a path: its last ending, and before it endings that
	// say what a part is (as .part7.rar.dms.part), not words of the name.
	extRE = regexp.MustCompile(`(?i)(?:\.(?:dms|part\d*|rar|7z|zip|tar|[rz]\d\d|\d{3})){0,3}\.[a-z0-9]{1,5}$`)
)

// scrubURL keeps a URL's scheme and host (with its port): where it went, not
// what was asked. strict: the scheme only.
func scrubURL(raw string, strict bool) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		scheme, _, _ := strings.Cut(raw, ":")
		return scheme + ":…"
	}
	if strict {
		return u.Scheme + "://…"
	}
	s := u.Scheme + "://" + u.Host
	if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
		s += "/…"
	}
	return s
}

// scrubPaths is one or more paths (a rename's old and new), each as its drive
// and file type: F:\…\x.rar.dms.part. %DATA% paths stay.
func scrubPaths(m string) string {
	starts := pathStartRE.FindAllStringIndex(m, -1)
	var b strings.Builder
	for i, st := range starts {
		end := len(m)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		p := m[st[0]:end]
		trail := p[len(strings.TrimRight(p, " ")):]
		p = strings.TrimRight(p, " ")
		name := p[strings.LastIndex(p, `\`)+1:]
		if strings.HasPrefix(p, `\\`) {
			b.WriteString(`\\…`) // the server's and the share's names are the user's too
		} else {
			b.WriteString(p[:3] + "…")
		}
		if ext := FileType(name); ext != "" {
			b.WriteString(`\x` + ext)
		}
		b.WriteString(trail)
	}
	return m[:starts[0][0]] + b.String()
}

// FileType is a name's endings that tell what it is (extRE), with the
// .dms.part of a file still downloading after them.
func FileType(name string) string {
	const part = ".dms.part"
	if len(name) > len(part) && strings.EqualFold(name[len(name)-len(part):], part) {
		return extRE.FindString(name[:len(name)-len(part)]) + part
	}
	return extRE.FindString(name)
}

var pathStartRE = regexp.MustCompile(`[A-Za-z]:\\|\\\\[^\\\s"]+\\`)

// scrubIP is an address that reaches the internet as what, else the address.
func scrubIP(m, as string) string {
	a, err := netip.ParseAddr(m)
	if err != nil || !a.IsGlobalUnicast() || a.IsPrivate() || cgnat.Contains(a.Unmap()) {
		return m
	}
	return as
}

// cgnat is 100.64.0.0/10: a carrier's or VPN's own addresses, not the user's.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")
