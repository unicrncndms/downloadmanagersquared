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
  // A part of a split archive is done once its set is extracted.
  return d.status === "completed" && !(d.folder && d.folder.set && !d.folder.set.finished);
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
// the files of one Metalink or the parts of one split archive together as a group, where the first of them
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
  const nDone = items.filter((d) => d.status === "completed").length;
  // A split archive's parts: its Set (engine) knows how many there are and
  // extracts them once fetched.
  const set = items.map((d) => d.folder && d.folder.set).find(Boolean);
  const unit = String(items[0].group || "").startsWith("parts:") ? "parts" : "files";
  const count = set && set.parts ? set.parts : n;
  const waiting = set && !set.finished; // every part fetched isn't the end: extracting, or a part missing
  const all = nDone === n && !waiting;
  const files = nDone === count ? `${count} ${unit}` : `${nDone} of ${count} ${unit} done`;
  let left = (size > 0 ? `${Math.round(Math.min(100, (done / size) * 100))}% · ${fmtBytes(done)} / ${fmtBytes(size)}` : fmtBytes(done)) + ` · ${files}`;
  if (waiting && set.of) left += ` · ${set.files} of ${set.of} files extracted`;
  if (waiting && set.note) left += ` · ${set.note}`;
  const can = (k) => items.some((d) => d.actions && d.actions[k]);
  let label = all ? "Completed" : items[0].label, tone = all ? "ok" : items[0].tone || "";
  if (waiting && nDone === n) [label, tone] = set.note ? ["Waiting", "warn"] : ["Extracting", ""];
  return {
    left, right: speed > 0 ? `↓ ${fmtSpeed(speed)}` : "",
    percent: size > 0 ? Math.min(100, (done / size) * 100) : 0,
    label, tone,
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
  // A RAR's count of files is only known once it is done: until then, how many so far.
  const sub = f && f.on && f.into && f.of > 0 ? `${f.files.toLocaleString("en-US")} / ${f.of.toLocaleString("en-US")} files`
    : f && f.on && f.into && f.files > 0 ? `${f.files.toLocaleString("en-US")} files extracted`
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

// The address families a connection is used with, as its badge says: "IPv4",
// "IPv6" or "IPv4 + IPv6". "" from an engine that doesn't say.
function familyText(i) {
  return (i.families || []).join(" + ");
}

// One hint per adapter left out because it has only IPv6 while IPv6 is off.
function ipv6Hints(state) {
  return ((state && state.ipv6Only) || []).map((name) =>
    `${name} has only IPv6: turn on Enable IPv6 (experimental) to use it.`);
}

// Check lines' answer: each line's public IP, and a warning per line shared:
// the same public IPv4 address, or for IPv6-only lines the same /64 (the
// engine's line; an older engine sends only ip).
function lineResults(checks) {
  const byLine = new Map();
  for (const c of checks || []) {
    const line = c.line || c.ip;
    if (line) byLine.set(line, [...(byLine.get(line) || []), c.name]);
  }
  return {
    lines: (checks || []).map((c) => `${c.name}: ${c.ip || "no answer (" + (c.error || "unknown") + ")"}`),
    warnings: [...byLine].filter(([, names]) => names.length > 1).map(([line, names]) =>
      `${andList(names)} reach the internet from the same ${line.endsWith("/64") ? "IPv6 network" : "public IP"} (${line}): they share one uplink, so together they are no faster than one.`),
  };
}

// announcements is what a screen reader is told as the list changes: a
// download finishing or failing, once. prev maps id to the status last seen;
// a download seen for the first time says nothing, so opening the popup
// doesn't read out old news. Returns the texts and the map for next time.
function announcements(prev, downloads) {
  const next = new Map();
  const texts = [];
  for (const d of downloads || []) {
    next.set(d.id, d.status);
    const was = prev && prev.get(d.id);
    if (!was || was === d.status) continue;
    const name = d.filename || d.url;
    if ((d.status === "completed" && was !== "seeding") || (d.status === "seeding" && was !== "completed")) texts.push(`${name} finished`);
    else if (d.status === "failed") texts.push(`${name} failed${d.error ? ": " + d.error : ""}`);
  }
  return { texts, seen: next };
}

// logLines is a download's activity log as lines, newest first: local date
// and time, the interface it concerns (if any), and what happened.
function logLines(events, nameOf) {
  const pad = (n) => String(n).padStart(2, "0");
  return (events || []).slice().reverse().map((e) => {
    const t = new Date(e.time);
    const when = `${t.getFullYear()}-${pad(t.getMonth() + 1)}-${pad(t.getDate())} ${pad(t.getHours())}:${pad(t.getMinutes())}:${pad(t.getSeconds())}`;
    return [when, e.iface ? nameOf(e.iface) : "", e.text].filter(Boolean).join(" · ");
  });
}

// matchesSearch is the downloads list's filter box: the words typed, in any
// case, found in the file name, the source link's host, the status badge or
// the Metalink's name. An empty box matches everything.
function matchesSearch(d, query) {
  const q = String(query || "").trim().toLowerCase();
  if (!q) return true;
  let host = "";
  try { host = new URL(d.url).hostname; } catch { /* a magnet link has none */ }
  return [d.filename, host, d.label, d.groupName].some((v) => v && String(v).toLowerCase().includes(q));
}

// addLines splits Add download's box into one link per non-empty line, with
// its line number; lines that aren't a link are returned in bad.
function addLines(text) {
  const urls = [], bad = [];
  String(text || "").split(/\r?\n/).forEach((raw, i) => {
    const url = raw.trim();
    if (!url) return;
    try {
      new URL(url);
      urls.push({ line: i + 1, url });
    } catch {
      bad.push({ line: i + 1, url });
    }
  });
  return { urls, bad };
}

if (typeof module !== "undefined") {
  module.exports = { addLines, matchesSearch, logLines, announcements, fmtBytes, fmtSpeed, fmtETA, percent, finished, sortDownloads, listEntries, groupView, mirrorRows, moveBefore, statusLine, problemLinks, viaLine, seedLine, bulkActions, routerWarnings, lineResults, familyText, ipv6Hints };
}
