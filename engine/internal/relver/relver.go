// Package relver is the rule for when the engine and the browser extension
// are of versions that may not work together; extension/lib/version.js has the
// same rule for the popup.
package relver

import (
	"regexp"
	"strconv"
)

var release = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

// majorMinor is the major and minor of "X.Y.Z", ok false for a development
// build ("dev", "0.0.0") or anything else that isn't a release version.
func majorMinor(v string) (major, minor int, ok bool) {
	m := release.FindStringSubmatch(v)
	if m == nil || (m[1] == "0" && m[2] == "0" && m[3] == "0") {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(m[1])
	minor, err2 := strconv.Atoi(m[2])
	return major, minor, err1 == nil && err2 == nil
}

// Clash is whether two versions differ in what counts: the major version, or
// before 1.0 the minor one. Patch releases never clash, and a development
// build on either side skips the check. An extension one minor newer than the
// engine is no clash: the stores publish the extension before GitHub publishes
// its engine, and each release keeps working with the engine before it
// (CONTRIBUTING.md). The other way round stays a clash.
func Clash(engine, extension string) bool {
	am, an, aok := majorMinor(engine)
	bm, bn, bok := majorMinor(extension)
	if !aok || !bok {
		return false
	}
	return am != bm || (am == 0 && an != bn && bn != an+1)
}
