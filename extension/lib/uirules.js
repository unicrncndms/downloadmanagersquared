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

if (typeof module !== "undefined") module.exports = { shortName, stripSpeed, unitField, sameFolder, canMoveFolder, browserNames };
