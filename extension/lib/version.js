"use strict";

// Whether the engine and the extension may not work together. Pure, no
// browser.*: the popup loads it as a script and the Node tests require it.

// [major, minor] of "X.Y.Z", or null for a development build ("dev", "0.0.0")
// or anything else that isn't a release version.
function releaseVersion(v) {
  const m = /^v?(\d+)\.(\d+)\.(\d+)$/.exec(v || "");
  if (!m || (m[1] === "0" && m[2] === "0" && m[3] === "0")) return null;
  return [Number(m[1]), Number(m[2])];
}

// True when engine and extension versions differ in what counts: the major
// version, or before 1.0 the minor one. Patch releases never clash, and a
// development build on either side skips the check.
function versionsClash(engine, extension) {
  const a = releaseVersion(engine);
  const b = releaseVersion(extension);
  if (!a || !b) return false;
  return a[0] !== b[0] || (a[0] === 0 && a[1] !== b[1]);
}

if (typeof module !== "undefined") module.exports = { versionsClash };
