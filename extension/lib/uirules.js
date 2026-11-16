"use strict";

// Small rules the pages (popup, card, settings) decide with, kept out of the
// page files so Node tests reach them. Pure, no browser.* or DOM: the pages
// load it as a script after list.js and the Node tests require it.

const uiLib = typeof module !== "undefined" ? require("./list.js") : { speedToUnit, finished };

// shortName fits a connection's name into the popup's strip: its first word,
// cut to three letters when long ("Ethernet" Eth, "Wi-Fi" stays).
function shortName(name) {
  const w = String(name).split(/\s+/)[0];
  return w.length > 6 ? w.slice(0, 3) : w;
}

// stripSpeed is a speed in the chosen unit with no unit shown, short enough
// for the strip: 4.4, 87, and from 1000 on 1.2G. The first chip names the unit.
function stripSpeed(bps) {
  const mb = uiLib.speedToUnit(bps);
  return mb >= 1000 ? (mb / 1000).toFixed(1) + "G" : mb.toFixed(mb < 100 ? 1 : 0);
}

// unitField is a speed limit as a field shows it: in the chosen unit, "" for none.
function unitField(bps) {
  return bps ? +uiLib.speedToUnit(bps).toFixed(3) : "";
}

// sameFolder: two folder paths are the same folder (Windows ignores case).
function sameFolder(a, b) {
  return (a || "").toLowerCase() === (b || "").toLowerCase();
}

// canMoveFolder: a download's folder can be changed only while nothing is
// being written: paused or queued, not a torrent, not Download as folder.
function canMoveFolder(d) {
  return d.kind !== "torrent" && !uiLib.finished(d) && !(d.folder && d.folder.on) && (d.status === "paused" || d.status === "queued");
}

// browserNames: "Firefox and Chrome" from the browsers' exe paths.
function browserNames(exes) {
  return [...new Set((exes || []).map((p) => p.split(/[\\/]/).pop().replace(/\.exe$/i, "")).filter(Boolean).map((n) => n[0].toUpperCase() + n.slice(1)))];
}

// partTiles reads a file map's d.mapPart: for each square, null when none of it
// is written (or it is whole), else { n: tenths written, 1 to 9, who: the index
// into mapBy of the connection that wrote it, -1 when not known }. A square
// with any byte on disk is never drawn as empty (the squares that fill only
// when whole would otherwise show 1.2 GB of 6.3 GB as three tiles).
function partTiles(part, count) {
  const out = [];
  for (let i = 0; i < count; i++) {
    const n = +((part || "")[2 * i] || 0), w = (part || "")[2 * i + 1] || "-";
    out.push(n > 0 ? { n: Math.min(9, n), who: w >= "a" && w <= "j" ? w.charCodeAt(0) - 97 : -1 } : null);
  }
  return out;
}

// frontFill is how full a square with a connection writing in it pulses (0 to
// 1): what is on disk of the whole square (part, from partTiles; null for none)
// when the engine sends mapPart. The front's own progress (the mapFill digit)
// counts from the square's start, so a square that starts with bytes still
// missing (a chunk begins inside it) would look nearly full and then drop to
// its real share when the front moves on.
function frontFill(digit, part, hasPart) {
  return hasPart ? (part ? part.n / 9 : 0) : (+digit || 0) / 9;
}

// rowFill is a share (0 to 100) as n squares in a row, each 0 (empty) to 1
// (full): the full ones, then one partly filled, then the empty ones.
function rowFill(pct, n) {
  const at = Math.max(0, Math.min(100, +pct || 0)) * n / 100;
  return Array.from({ length: n }, (_, i) => Math.max(0, Math.min(1, at - i)));
}

if (typeof module !== "undefined") module.exports = { rowFill, frontFill, partTiles, shortName, stripSpeed, unitField, sameFolder, canMoveFolder, browserNames };
