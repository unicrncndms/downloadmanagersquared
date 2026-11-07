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

// sortDownloads orders the engine's list (in queue order) by group, newest
// first within each, except the queued: those in the order they will start.
function sortDownloads(downloads) {
  return downloads.map((d, i) => ({ d, i }))
    .sort((a, b) => group(a.d) - group(b.d) || (group(a.d) === 3 ? a.i - b.i : b.i - a.i))
    .map((x) => x.d);
}

// listEntries is what the list draws, in order: a download on its own, or
// the files of one Metalink together as a group, where the first of them
// would be. A group down to one file shows that file on its own.
function listEntries(sorted) {
  const out = [];
  const groups = new Map();
  for (const d of sorted) {
    if (!d.group) { out.push({ d }); continue; }
    let g = groups.get(d.group);
    if (!g) {
      g = { group: d.group, name: d.groupName || "Metalink", items: [] };
      groups.set(d.group, g);
      out.push(g);
    }
    g.items.push(d);
  }
  return out.map((e) => (e.items && e.items.length === 1 ? { d: e.items[0] } : e));
}

// groupView is a group card's lines: its files added up, the badge of the
// file that needs the most attention (items come sorted, so the first), and
// what its buttons can do to the files.
function groupView(items) {
  const n = items.length;
  const known = items.every((d) => d.size > 0);
  const size = known ? items.reduce((a, d) => a + d.size, 0) : -1;
  const done = items.reduce((a, d) => a + (d.done || 0), 0);
  const speed = items.reduce((a, d) => a + (d.status === "running" ? d.speed || 0 : 0), 0);
  const nDone = items.filter(finished).length;
  const all = nDone === n;
  const files = all ? `${n} files` : `${nDone} of ${n} files done`;
  const left = (size > 0 ? `${Math.round(Math.min(100, (done / size) * 100))}% · ${fmtBytes(done)} / ${fmtBytes(size)}` : fmtBytes(done)) + ` · ${files}`;
  const can = (k) => items.some((d) => d.actions && d.actions[k]);
  return {
    left, right: speed > 0 ? `↓ ${fmtSpeed(speed)}` : "",
    percent: size > 0 ? Math.min(100, (done / size) * 100) : 0,
    label: all ? "Completed" : items[0].label, tone: all ? "ok" : items[0].tone || "",
    pause: can("pause"), resume: can("resume"), finished: all,
    partialFile: items.some((d) => d.partialFile),
  };
}

// mirrorRows are a download's mirrors for Details: host, which interfaces
// use it (nameOf names one), and why it was dropped.
function mirrorRows(d, nameOf) {
  return (d.mirrors || []).map((m) => {
    let host = m.url;
    try { host = new URL(m.url).host; } catch (e) { /* the URL as it is */ }
    const users = Object.keys(d.links || {}).filter((id) => d.links[id].mirror === host).map(nameOf).sort();
    return { host, url: m.url, users, dropped: m.dropped || "" };
  });
}

// moveBefore is the engine "move" a queued download id takes, as its
// "before": dropped on target, it takes target's place (after it when
// dragged down); target "first" or "last" is an end of the queue. null:
// nothing to move.
function moveBefore(downloads, id, target) {
  const qs = downloads.filter((d) => d.status === "queued").map((d) => d.id);
  const from = qs.indexOf(id);
  if (from < 0) return null;
  if (target === "first") return from === 0 ? null : qs[0];
  if (target === "last") return from === qs.length - 1 ? null : "";
  const to = qs.indexOf(target);
  if (to < 0 || to === from) return null;
  return to < from ? target : qs[to + 1] || "";
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

// "Ethernet and Wi-Fi": names joined for a sentence.
function andList(names) {
  return names.length < 2 ? names.join("") : `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

// Interfaces that look like two lines but add no bandwidth: one warning per
// group behind the same router (the engine's sameRouter, by gateway MAC).
// Never a reason to turn one off: the user decides.
function routerWarnings(interfaces) {
  const groups = new Map();
  for (const i of interfaces || []) {
    if (i.tunnel || !(i.sameRouter || []).length) continue;
    const names = [i.name, ...i.sameRouter].sort();
    groups.set(names.join("\n"), names);
  }
  return [...groups.values()].map((names) =>
    `${andList(names)} go through the same router: together they are no faster than one of them.`);
}

// Check lines' answer: each line's public IP, and a warning per IP shared.
function lineResults(checks) {
  const byIP = new Map();
  for (const c of checks || []) if (c.ip) byIP.set(c.ip, [...(byIP.get(c.ip) || []), c.name]);
  return {
    lines: (checks || []).map((c) => `${c.name}: ${c.ip || "no answer (" + (c.error || "unknown") + ")"}`),
    warnings: [...byIP].filter(([, names]) => names.length > 1).map(([ip, names]) =>
      `${andList(names)} reach the internet from the same public IP (${ip}): they share one uplink, so together they are no faster than one.`),
  };
}

if (typeof module !== "undefined") {
  module.exports = { fmtBytes, fmtSpeed, fmtETA, percent, finished, sortDownloads, listEntries, groupView, mirrorRows, moveBefore, statusLine, problemLinks, viaLine, seedLine, bulkActions, routerWarnings, lineResults };
}
