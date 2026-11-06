"use strict";

// What the download list shows at a glance, and in what order. Pure, no
// browser.* or DOM: the pages load it as a script and the Node tests require
// it. It also holds the number formats every page uses. d is a download from
// the engine's state; settings the engine's settings.

function fmtBytes(n) {
  if (!(n >= 0)) return "?";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1000 && i < u.length - 1) { n /= 1000; i++; }
  return `${n.toFixed(i && n < 100 ? 1 : 0)} ${u[i]}`;
}

function fmtSpeed(bps) {
  return bps >= 1 ? fmtBytes(bps) + "/s" : "0 B/s";
}

function fmtETA(sec) {
  if (!(sec >= 0) || !isFinite(sec)) return "—";
  const h = Math.floor(sec / 3600), m = Math.floor(sec / 60) % 60, s = Math.floor(sec % 60);
  if (h) return `${h}h ${String(m).padStart(2, "0")}m`;
  if (m) return `${m}m ${String(s).padStart(2, "0")}s`;
  return `${s}s`;
}

function percent(d) {
  return d.size > 0 ? Math.min(100, (d.done / d.size) * 100) : 0;
}

// finished: complete and done with, so it shrinks to one line. A seeding
// torrent is still at work.
function finished(d) {
  return d.status === "completed";
}

// The groups, in the order they are listed: what needs you (failed, a pause
// with a reason, a torrent waiting for its files, a Download as folder
// waiting for its folder), then downloading, seeding, queued, paused,
// finished.
function group(d) {
  const asking = d.kind !== "torrent" && d.folder && d.folder.on && !d.folder.into && d.status !== "completed";
  if (d.status === "failed" || d.status === "choosing" || asking || (d.status === "paused" && d.error)) return 0;
  switch (d.status) {
    case "running": case "verifying": case "metadata": case "checking": return 1;
    case "seeding": return 2;
    case "queued": return 3;
    case "paused": return 4;
    case "completed": return 5;
  }
  return 1;
}

// sortDownloads orders the engine's list (oldest first) by group, newest first
// within each.
function sortDownloads(downloads) {
  return downloads.map((d, i) => ({ d, i }))
    .sort((a, b) => group(a.d) - group(b.d) || b.i - a.i)
    .map((x) => x.d);
}

// statusLine is the line under the progress bar: left "31% · 13.0 GB / 42.0
// GB", right speed and time left while running, sub a small second line (a
// Download as folder's files, else the engine's note), error in place of it
// all when there is one.
function statusLine(d) {
  let left = "";
  if (d.size > 0) left = `${Math.round(percent(d))}% · ${fmtBytes(d.done)} / ${fmtBytes(d.size)}`;
  else if (d.done > 0) left = fmtBytes(d.done);
  const right = d.status === "running" ? `↓ ${fmtSpeed(d.speed)} · ${fmtETA(d.eta)} left` : "";
  const f = d.kind !== "torrent" && d.folder;
  const sub = f && f.on && f.into && f.of > 0
    ? `${f.files.toLocaleString("en-US")} / ${f.of.toLocaleString("en-US")} files`
    : d.note && d.note !== "complete" ? d.note : "";
  return { left, right, sub, error: d.error || "" };
}

// problemLinks are the interfaces worth a row on the card: failed, blocked or
// waiting (server busy, backing off, VPN down). The rest are in Details.
function problemLinks(d) {
  if (finished(d)) return [];
  return Object.keys(d.links || {}).filter((id) => {
    const l = d.links[id];
    return !!l.error || !!l.canRetry || l.tone === "warn" || l.tone === "bad";
  });
}

// viaLine is a short line of the interfaces a running download uses, busiest
// first, with each one's connections (peers for a torrent): "via Ethernet 4×
// · Wi-Fi 2×". "" when it isn't running or nothing is connected. nameOf gives
// an interface's name.
function viaLine(d, nameOf) {
  if (d.status !== "running" && d.status !== "seeding" && d.status !== "metadata") return "";
  const torrent = d.kind === "torrent";
  const used = Object.keys(d.links || {}).filter((id) => d.links[id].conns > 0)
    .sort((a, b) => d.links[b].conns - d.links[a].conns || (d.links[b].speed || 0) - (d.links[a].speed || 0));
  if (!used.length) return "";
  return "via " + used.map((id) => {
    const n = d.links[id].conns;
    return `${nameOf(id)} ${torrent ? `${n} peer${n === 1 ? "" : "s"}` : `${n}×`}`;
  }).join(" · ");
}

// seedLine is a torrent's upload and ratio, against the seed ratio target
// while seeding; "" before it has uploaded anything.
function seedLine(d, settings) {
  if (d.kind !== "torrent") return "";
  const seeding = d.status === "seeding";
  if (!seeding && !(d.uploaded > 0)) return "";
  const target = seeding && settings && settings.seedRatio > 0 ? ` / ${settings.seedRatio.toFixed(2)}` : "";
  return `↑ ${fmtSpeed(d.upSpeed || 0)} · ratio ${(d.ratio || 0).toFixed(2)}${target}`;
}

// bulkActions: Pause all and Resume all show only while two or more
// downloads can use them.
function bulkActions(downloads) {
  const n = (k) => downloads.filter((d) => d.actions && d.actions[k]).length;
  return { pauseAll: n("pause") >= 2, resumeAll: n("resume") >= 2 };
}

if (typeof module !== "undefined") {
  module.exports = { fmtBytes, fmtSpeed, fmtETA, percent, finished, sortDownloads, statusLine, problemLinks, viaLine, seedLine, bulkActions };
}
