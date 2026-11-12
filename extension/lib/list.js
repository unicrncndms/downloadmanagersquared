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
  if (h >= 48) return `${Math.floor(h / 24)}d ${h % 24}h`;
  if (h) return `${h}h ${String(m).padStart(2, "0")}m`;
  if (m) return `${m}m ${String(s).padStart(2, "0")}s`;
  return `${s}s`;
}

function percent(d) {
  return d.size > 0 ? Math.min(100, (d.done / d.size) * 100) : 0;
}

// The list's sections, in the order they are listed. The engine puts each
// download in one (its "section", see Present in the engine); the headings are
// ours. Folded sections are saved by heading, so the words must not change.
const SECTION_KEYS = ["needs", "active", "seeding", "queued", "paused", "done"];
const SECTIONS = { needs: "Needs you", active: "Downloading", seeding: "Seeding", queued: "Queue", paused: "Paused", done: "Finished" };

// finished: complete and done with, so it shrinks to one line. A seeding
// torrent is still at work, and so is a part whose archive isn't extracted.
function finished(d) {
  return d.section === "done";
}

// group is a download's place in the list: its section's number.
function group(d) {
  const i = SECTION_KEYS.indexOf(d.section);
  return i < 0 ? 1 : i;
}

// tileOf is a row's status tile: its tone (need, run, seed, done, or plain)
// and its icon. A queued one shows its place in the queue instead ("").
function tileOf(d) {
  if (finished(d)) return { kind: "done", icon: "check" };
  return [
    { kind: "need", icon: "alert" }, { kind: "run", icon: "down" }, { kind: "seed", icon: "up" },
    { kind: "", icon: "" }, { kind: "", icon: "pause" },
  ][Math.min(group(d), 4)];
}

// rowNums are the two numbers at the end of a download's row: what matters
// most now, and under it what places it. Running: the speed, then how far and
// how long. At rest: how far, then the bytes. Not begun or finished: its size.
function rowNums(d) {
  const pct = d.size > 0 ? `${Math.round(percent(d))}%` : "";
  if (finished(d) || (d.status === "queued" && !d.done)) return { main: d.size > 0 ? fmtBytes(d.size) : "", sub: "" };
  if (d.status === "seeding") return { main: "", sub: "" }; // its line says what it uploads
  if (d.status === "running") return { main: fmtSpeed(d.speed), sub: [pct, `${fmtETA(d.eta)} left`].filter(Boolean).join(" · ") };
  return { main: pct || (d.done > 0 ? fmtBytes(d.done) : ""), sub: d.size > 0 ? `${fmtBytes(d.done)} / ${fmtBytes(d.size)}` : "" };
}

// groupView is a group card's lines: the engine's summary of its files, with
// the numbers worded.
function groupView(g) {
  const unit = g.parts ? "parts" : "files";
  const files = g.done === g.count ? `${g.count} ${unit}` : `${g.done} of ${g.count} ${unit} done`;
  const percent = g.size > 0 ? Math.min(100, (g.got / g.size) * 100) : 0;
  let left = (g.size > 0 ? `${Math.round(percent)}% · ${fmtBytes(g.got)} / ${fmtBytes(g.size)}` : fmtBytes(g.got)) + ` · ${files}`;
  if (g.speed > 0 && g.eta > 0) left += ` · ${fmtETA(g.eta)} left`;
  return {
    left, right: g.speed > 0 ? `↓ ${fmtSpeed(g.speed)}` : "", speed: g.speed, percent,
    label: g.label, tone: g.tone || "", pause: g.pause, resume: g.resume, finished: g.finished, partialFile: g.partialFile,
  };
}

// extractView is a Download as folder's extraction, for its own bar under
// the download's: how much of the archive is extracted, and a line saying
// what it is doing or waiting for (the engine's words, ADR 0003) with the
// time left. items: one download, or a split archive's parts. null when
// nothing is being extracted.
// eta: a split archive's, from its group summary (the later of its pace and
// the download's), in place of the Set's own pace.
function extractView(items, eta) {
  // Labelled, as it sits under the download's own bar: while extracting, how
  // far it is; else what it waits for (the engine's words).
  const line = (bytes, total, what, eta, files) => {
    const percent = total > 0 ? Math.min(100, ((bytes || 0) / total) * 100) : 0;
    const where = what === "Extracting" ? `${Math.round(percent)}%` : what;
    return { percent, text: ["Extraction", where, files, eta > 0 ? `${fmtETA(eta)} left` : ""].filter(Boolean).join(" · ") };
  };
  if (!items.some((d) => d.kind !== "torrent" && d.folder && d.folder.on && d.folder.into)) return null;
  const set = items.map((d) => d.folder.set).find(Boolean);
  if (set) {
    if (set.finished || !(set.note || set.phase)) return null;
    // A part missing or a password wanted: no time left to give.
    return line(set.extracted, set.total, set.note || set.phase, set.note ? 0 : eta || set.eta,
      set.of ? `${set.files} of ${set.of} files` : "");
  }
  const d = items[0];
  if (items.length !== 1 || !d.folder.phase) return null;
  return line(d.folder.extracted, d.size, d.folder.phase, d.folder.extractEta);
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
// dragged down); target "first" or "last" is an end of the queue, "up" or
// "down" one place. Dropped on a download that is running, it takes its slot
// (the engine queues that one behind it). null: nothing to move.
function moveBefore(downloads, id, target) {
  const qs = downloads.filter((d) => d.status === "queued").map((d) => d.id);
  const from = qs.indexOf(id);
  if (from < 0) return null;
  if (target === "first") return from === 0 ? null : qs[0];
  if (target === "last") return from === qs.length - 1 ? null : "";
  if (target === "up") return from === 0 ? null : qs[from - 1];
  if (target === "down") return from === qs.length - 1 ? null : qs[from + 2] || "";
  if (downloads.some((d) => d.id === target && d.status === "running")) return target;
  const to = qs.indexOf(target);
  if (to < 0 || to === from) return null;
  return to < from ? target : qs[to + 1] || "";
}

// predict is the engine's state as request method(p) will leave it, so the
// page shows a click's result at once instead of when the engine answers (a
// pause answers only once its connections have closed). Only what the page
// can know: the engine's next state replaces it. The same state back when
// there is nothing to show early.
function predict(state, method, p) {
  const ds = (state && state.downloads) || [];
  p = p || {};
  const me = ds.find((d) => d.id === p.id);
  const map = (pick, f) => (ds.some(pick) ? { ...state, downloads: ds.map((d) => (pick(d) ? f(d) : d)) } : state);
  const can = (a) => (d) => !!(d.actions && d.actions[a]);
  // The three labels mirror statusText in engine/internal/download/present.go.
  const stopped = (status, label) => (d) => ({ ...d, status, section: status, label, plain: true, tone: "dim", speed: 0, eta: -1, error: status === "queued" ? "" : d.error,
    actions: { ...d.actions, pause: status === "queued", resume: status !== "queued" } });
  const paused = stopped("paused", "Paused"), queued = stopped("queued", "Queued");
  switch (method) {
    case "pause": return map((d) => d === me && can("pause")(d), paused);
    case "resume": return map((d) => d === me && can("resume")(d), queued);
    case "pauseAll": return map(can("pause"), paused);
    case "resumeAll": return map(can("resume"), queued);
    case "remove": return me ? { ...state, downloads: ds.filter((d) => d !== me) } : state;
    case "setLimit": return map((d) => d === me, (d) => ({ ...d, limit: p.limit }));
    case "setFolder": {
      // A split archive's parts have one switch and one folder.
      const set = me && String(me.group || "").startsWith("parts:") ? me.group : null;
      const patch = { ...("on" in p ? { on: !!p.on } : {}), ...("into" in p ? { into: p.into } : {}) };
      return map((d) => !!d.folder && (d === me || (set && d.group === set)), (d) => ({ ...d, folder: { ...d.folder, ...patch } }));
    }
    case "move": {
      const b = ds.find((d) => d.id === p.before);
      if (!me || me === b || (p.before && !b)) return state;
      // Before one that is downloading: it takes its slot, and that one queues behind it.
      const swap = b && b.status === "running" && me.status === "queued";
      const rest = ds.filter((d) => d !== me).map((d) => (swap && d === b ? queued(d) : d));
      const at = b ? ds.filter((d) => d !== me).indexOf(b) : rest.length;
      rest.splice(at, 0, swap ? { ...me, status: "running", section: "active", label: "Downloading", plain: true, tone: "" } : me);
      return { ...state, downloads: rest };
    }
  }
  return state;
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
  const torrent = d.kind === "torrent";
  const used = viaLinks(d);
  if (!used.length) return "";
  return "via " + used.map((id) => {
    const n = d.links[id].conns;
    return `${nameOf(id)} ${torrent ? `${n} peer${n === 1 ? "" : "s"}` : `${n}×`}`;
  }).join(" · ");
}

// viaLinks are the interfaces a running download uses, busiest first; none
// when it isn't running or nothing is connected.
function viaLinks(d) {
  if (d.status !== "running" && d.status !== "seeding" && d.status !== "metadata") return [];
  return Object.keys(d.links || {}).filter((id) => d.links[id].conns > 0)
    .sort((a, b) => d.links[b].conns - d.links[a].conns || (d.links[b].speed || 0) - (d.links[a].speed || 0));
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

// addView is what the Add box shows for the text in it: how many links, and
// which choices apply to them. A landing page only to links to web pages or
// files (not a magnet), the VPN to any link.
function addView(text) {
  const { urls, bad } = addLines(text);
  const web = urls.some((u) => /^https?:/i.test(u.url));
  const n = urls.length;
  return { count: n, bad: bad.length, page: web, vpn: n > 0, label: n > 1 ? `Add ${n} downloads` : "Add" };
}

if (typeof module !== "undefined") {
  module.exports = { addLines, addView, matchesSearch, logLines, announcements, fmtBytes, fmtSpeed, fmtETA, percent, finished, groupView, extractView, predict, viaLinks, group, SECTIONS, SECTION_KEYS, tileOf, rowNums, mirrorRows, moveBefore, statusLine, problemLinks, viaLine, seedLine, bulkActions, routerWarnings, lineResults, familyText, ipv6Hints };
}
