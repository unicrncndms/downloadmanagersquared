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
// development build on either side skips the check. An extension one minor
// newer than the engine is no clash (engineBehind): the stores publish the
// extension before GitHub publishes its engine, and each release keeps working
// with the engine before it (CONTRIBUTING.md). Same rule as the engine's
// relver.Clash.
function versionsClash(engine, extension) {
  const a = releaseVersion(engine);
  const b = releaseVersion(extension);
  if (!a || !b) return false;
  return a[0] !== b[0] || (a[0] === 0 && a[1] !== b[1] && !engineBehind(engine, extension));
}

// True when the extension is exactly one minor newer than the engine, same
// major: they work together, and the engine's release is out or on its way.
function engineBehind(engine, extension) {
  const a = releaseVersion(engine), b = releaseVersion(extension);
  return !!a && !!b && a[0] === b[0] && b[1] === a[1] + 1;
}

// "engine" or "extension": whichever of the two clashing versions is older.
function olderSide(engine, extension) {
  const a = releaseVersion(engine), b = releaseVersion(extension);
  if (!a || !b) return "";
  return a[0] !== b[0] ? (a[0] < b[0] ? "engine" : "extension") : a[1] < b[1] ? "engine" : "extension";
}

// The note after an upgrade, from the engine version last acknowledged (the
// engine's seenVersion setting): "record" the first time ever (nothing to
// announce), "show" once the engine version changed, "" otherwise.
function updateNote(seen, engine) {
  if (!releaseVersion(engine)) return "";
  if (!seen) return "record";
  return seen === engine ? "" : "show";
}

// The release page of version v, from the "latest" one.
function releaseNotesURL(latestURL, v) {
  return latestURL.replace(/\/latest$/, "/tag/v" + String(v).replace(/^v/, ""));
}

if (typeof module !== "undefined") module.exports = { versionsClash, engineBehind, olderSide, updateNote, releaseNotesURL };
