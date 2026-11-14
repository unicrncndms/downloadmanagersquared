// Package relver is the rule for when the engine and the browser extension
// are of versions that may not work together; extension/lib/version.js has the
// same rule for the popup.
package relver

import "regexp"

var release = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

// majorMinor is the major and minor of "X.Y.Z", ok false for a development
// build ("dev", "0.0.0") or anything else that isn't a release version.
func majorMinor(v string) (major, minor string, ok bool) {
	m := release.FindStringSubmatch(v)
	if m == nil || (m[1] == "0" && m[2] == "0" && m[3] == "0") {
		return "", "", false
	}
	return trim(m[1]), trim(m[2]), true
}

// trim drops leading zeros, so "01" and "1" are the same number.
func trim(n string) string {
	for len(n) > 1 && n[0] == '0' {
		n = n[1:]
	}
	return n
}

// Clash is whether two versions differ in what counts: the major version, or
// before 1.0 the minor one. Patch releases never clash, and a development
// build on either side skips the check.
func Clash(engine, extension string) bool {
	am, an, aok := majorMinor(engine)
	bm, bn, bok := majorMinor(extension)
	if !aok || !bok {
		return false
	}
	return am != bm || (am == "0" && an != bn)
}
