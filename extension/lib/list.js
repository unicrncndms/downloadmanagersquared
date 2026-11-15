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

// The unit speeds and limits are shown in: the engine's speedUnit setting,
// "MBps" (bytes, the default) or "Mbps" (bits). The pages set it from the
// settings (setSpeedUnit); stored limits stay bytes per second. The Go tray has
// the same rule (tray.Speed).
let speedUnit = "MBps";
function setSpeedUnit(u) { speedUnit = u === "Mbps" ? "Mbps" : "MBps"; }
function speedUnitLabel(unit = speedUnit) { return unit === "Mbps" ? "Mbit/s" : "MB/s"; }
// speedToUnit / speedFromUnit convert bytes per second to a number in the unit (Mbit/s = bytes/s * 8 / 1e6) and back.
function speedToUnit(bps, unit = speedUnit) { return bps * (unit === "Mbps" ? 8 : 1) / 1e6; }
function speedFromUnit(v, unit = speedUnit) { return Math.round(v * 1e6 / (unit === "Mbps" ? 8 : 1)); }

function fmtSpeed(bps, unit = speedUnit) {
  if (unit !== "Mbps") return bps >= 1 ? fmtBytes(bps) + "/s" : "0 B/s";
  if (!(bps >= 1)) return "0 bit/s";
  let n = bps * 8, i = 0;
  const u = ["bit", "kbit", "Mbit", "Gbit"];
  while (n >= 1000 && i < u.length - 1) { n /= 1000; i++; }
  return `${n.toFixed(i && n < 100 ? 1 : 0)} ${u[i]}/s`;
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

// "12 MB / 80 MB": how much of how much.
function progressText(done, size) {
  return `${fmtBytes(done)} / ${fmtBytes(size)}`;
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
  if (d.status === "choosing") return { kind: "pick", icon: "folder" }; // a first step, not a failure
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
  return { main: pct || (d.done > 0 ? fmtBytes(d.done) : ""), sub: d.size > 0 ? progressText(d.done, d.size) : "" };
}

// groupView is a group card's lines: the engine's summary of its files, with
// the numbers worded.
function groupView(g) {
  const unit = g.parts ? "parts" : "files";
  const files = g.done === g.count ? `${g.count} ${unit}` : `${g.done} of ${g.count} ${unit} done`;
  const percent = g.size > 0 ? Math.min(100, (g.got / g.size) * 100) : 0;
  let left = (g.size > 0 ? `${Math.round(percent)}% · ${progressText(g.got, g.size)}` : fmtBytes(g.got)) + ` · ${files}`;
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
    case "startNow": return map((d) => d === me && d.status === "queued", (d) => ({ ...d, status: "running", section: "active", label: "Downloading", plain: true, tone: "" }));
    case "remove": return me ? { ...state, downloads: ds.filter((d) => d !== me) } : state;
    case "removeMany": return ds.some((d) => (p.ids || []).includes(d.id)) ? { ...state, downloads: ds.filter((d) => !(p.ids || []).includes(d.id)) } : state;
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
  if (d.size > 0) left = `${Math.round(percent(d))}% · ${progressText(d.done, d.size)}`;
  else if (d.done > 0) left = fmtBytes(d.done);
  const right = d.status === "running" ? `↓ ${fmtSpeed(d.speed)} · ${fmtETA(d.eta)} left` : "";
  const f = d.kind !== "torrent" && d.folder;
  // A RAR's count of files is only known once it is done: until then, how many so far.
  const sub = f && f.on && f.into && f.of > 0 ? `${f.files.toLocaleString("en-US")} / ${f.of.toLocaleString("en-US")} files`
    : f && f.on && f.into && f.files > 0 ? `${f.files.toLocaleString("en-US")} files extracted`
    : d.note && d.note !== "complete" ? d.note
    : d.status === "running" && d.limitedBy ? `limited to ${fmtSpeed(d.limitedTo)} by ${d.limitedBy}` : "";
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

// viaLinks are the interfaces a running download uses, in the order of
// `order` (the engine's interface list; unlisted ones after, by ID) so the
// tiles keep their places as the counts change; none when it isn't running or
// nothing is connected.
function viaLinks(d, order = []) {
  if (d.status !== "running" && d.status !== "seeding" && d.status !== "metadata") return [];
  const place = (id) => { const i = order.indexOf(id); return i < 0 ? order.length : i; };
  return Object.keys(d.links || {}).filter((id) => d.links[id].conns > 0)
    .sort((a, b) => place(a) - place(b) || (a < b ? -1 : a > b ? 1 : 0));
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
function routerGroups(interfaces) {
  const groups = new Map();
  for (const i of interfaces || []) {
    if (i.tunnel || !(i.sameRouter || []).length) continue;
    const names = [i.name, ...i.sameRouter].sort();
    groups.set(names.join("\n"), names);
  }
  return [...groups.values()];
}

function routerWarnings(interfaces) {
  return routerGroups(interfaces).map((names) =>
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

// STATE_WORDS are the search words "is:failed" and the like: the state a
// download has, whatever the section it sits in.
const STATE_WORDS = {
  failed: (d) => d.status === "failed",
  paused: (d) => d.status === "paused",
  queued: (d) => d.status === "queued",
  running: (d) => d.status === "running",
  done: (d) => finished(d),
};

// matchesSearch is the downloads list's filter box: the words typed, in any
// case, found in the file name, the source link's host, the status badge or
// the Metalink's name. An empty box matches everything. "is:failed", "is:paused",
// "is:queued", "is:running" and "is:done" take a state instead (several: any
// of them); with other words, both must hold.
function matchesSearch(d, query) {
  const { states, q } = typeof query === "object" && query ? query : parseQuery(query);
  if (states.length && !states.some((s) => STATE_WORDS[s](d))) return false;
  if (!q) return true;
  return [d.filename, hostOf(d.url), d.label, d.groupName].some((v) => v && String(v).toLowerCase().includes(q));
}

// parseQuery splits the box into its states and its words once, so a list of
// thousands is not parsed once per download. matchesSearch takes it too.
function parseQuery(query) {
  const words = String(query || "").trim().toLowerCase().split(/\s+/).filter(Boolean);
  const isState = (w) => w.startsWith("is:") && Object.hasOwn(STATE_WORDS, w.slice(3));
  return { states: words.filter(isState).map((w) => w.slice(3)), q: words.filter((w) => !isState(w)).join(" ") };
}

// hostOf is a link's host name ("" for a magnet link), remembered: the same
// links are searched again every second.
const hosts = new Map();
function hostOf(url) {
  let h = hosts.get(url);
  if (h === undefined) {
    try { h = new URL(url).hostname; } catch { h = ""; }
    if (hosts.size > 5000) hosts.clear();
    hosts.set(url, h);
  }
  return h;
}

// errorSource is the connection a download's error belongs to, so the row can
// lead with its dot and name: the one whose own error is the download's, else
// the only one that has an error. "" when it can't say.
function errorSource(d) {
  const ids = Object.keys(d.links || {}).filter((id) => d.links[id].error);
  return ids.find((id) => d.links[id].error === d.error) || (ids.length === 1 ? ids[0] : "");
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

// addFileCheck: why a local file can't be added, or "" when it can. The file
// goes to the engine as base64 in one message, and Firefox takes 1 MB from an
// extension to its host: 700 kB of file is 933 kB of base64.
const MAX_ADD_FILE = 700000;
function addFileCheck(name, size) {
  if (!/\.(torrent|meta4|metalink)$/i.test(String(name))) return `${name}: only .torrent, .meta4 and .metalink files can be added`;
  if (size > MAX_ADD_FILE) return `${name} is too big to add from here (over 700 kB). Put it on a web page and add its link instead.`;
  return "";
}

// addSubmits: whether a key in the Add box adds the links. Ctrl+Enter always;
// a plain Enter when the box holds one line (more lines: Enter is a new line).
function addSubmits(e, text) {
  if (e.key !== "Enter" || e.shiftKey || e.altKey || e.isComposing) return false;
  return !!(e.ctrlKey || e.metaKey) || !/\n/.test(String(text || "").trim());
}

// addView is what the Add box shows for the text in it: how many links, and
// which choices apply to them. A landing page only to links to web pages or
// files (not a magnet), the VPN to any link.
// plainFtpNote: the warning for an ftp:// link with a password in it, which
// FTP sends unencrypted; "" when none of the links has one.
function plainFtpNote(text) {
  const bad = addLines(text).urls.some(({ url }) => /^ftp:\/\/[^/?#]*:[^/?#]*@/i.test(url));
  return bad ? "An ftp:// link sends its password and the file unencrypted. Use ftps:// if the server has it." : "";
}

function addView(text) {
  const { urls, bad } = addLines(text);
  const web = urls.some((u) => /^https?:/i.test(u.url));
  const n = urls.length;
  return { count: n, bad: bad.length, page: web, vpn: n > 0, label: n > 1 ? `Add ${n} downloads` : "Add" };
}

// finishedSummary is a finished download's one line: "3.0 GB in 4m 10s · 12 MB/s
// · Ethernet 62%, Wi-Fi 38%". The time is what the download ran (d.active, from
// the engine), not the time since it was added: queued and paused time is no
// speed. Without it (an old download) only the size; shares only when more
// than one interface fetched bytes. "" when it can't say.
function finishedSummary(d, nameOf) {
  const secs = d.active;
  const parts = [];
  if (d.size > 0) parts.push(secs > 0 ? `${fmtBytes(d.size)} in ${fmtETA(secs)}` : fmtBytes(d.size));
  if (d.size > 0 && secs > 0) parts.push(fmtSpeed(d.size / secs));
  const by = Object.keys(d.links || {}).filter((id) => d.links[id].bytes > 0).sort((a, b) => d.links[b].bytes - d.links[a].bytes);
  const total = by.reduce((n, id) => n + d.links[id].bytes, 0);
  if (by.length > 1) parts.push(by.map((id) => `${nameOf(id)} ${Math.round((d.links[id].bytes / total) * 100)}%`).join(", "));
  return parts.join(" · ");
}

// linesNote is what the empty list says of the lines found (ready interfaces):
// how many, and whether they look separate. `add` asks for the second-line hint
// (one line), `check` offers Check lines (several lines that look separate).
// Lines behind one router say so and promise no speed (shared: true).
function linesNote(ready) {
  const names = ready.map((i) => i.name);
  if (names.length < 2) return { text: "One line found. A second line (USB tether, hotspot) is what adds speed.", add: true, check: false, shared: false };
  const same = routerGroups(ready);
  if (same.length) return { text: `${names.length} lines found. ${same.map((g) => `${andList(g)} share one line and will not add speed.`).join(" ")}`, add: false, check: true, shared: true };
  return { text: `${names.length} lines found, they look separate (${andList(names)}).`, add: false, check: true, shared: false };
}

// finishedVerdict is one factual line after a multi-connection download: two
// that share a router gained nothing, else the smallest share "added N%".
// "" when one connection fetched, or the engine doesn't say.
function finishedVerdict(d, interfaces, nameOf) {
  const by = Object.keys(d.links || {}).filter((id) => d.links[id].bytes > 0);
  if (by.length < 2) return "";
  const total = by.reduce((n, id) => n + d.links[id].bytes, 0);
  const info = (id) => (interfaces || []).find((i) => i.id === id);
  for (const a of by) for (const b of by) {
    if (a < b && info(a) && (info(a).sameRouter || []).includes(nameOf(b))) return `${andList([nameOf(a), nameOf(b)])} share one line: no gain`;
  }
  const least = by.sort((a, b) => d.links[a].bytes - d.links[b].bytes)[0];
  return `${nameOf(least)} added ${Math.round((d.links[least].bytes / total) * 100)}%`;
}

// badgeOf is the toolbar button's badge for the engine's downloads: "!" on a
// warning colour while one needs the user or failed and the list has not
// shown it yet (seen: a Set of ids), else how many run.
function badgeOf(downloads, seen = new Set()) {
  const ds = downloads || [];
  if (needsYou(ds).some((d) => !seen.has(d.id))) return { text: "!", color: "#d97706" };
  const n = ds.filter((d) => d.status === "running").length;
  return { text: n ? String(n) : "", color: "#2563eb" };
}

// needsYou are the downloads that wait for the user or failed.
function needsYou(downloads) {
  return (downloads || []).filter((d) => d.section === "needs" || d.status === "failed");
}

// seenAfter is the seen Set after a state update: ids that still need the user
// stay seen; an id leaves once its download runs again or is gone. With opened
// (the list was just opened), every download that needs the user is seen.
function seenAfter(downloads, seen, opened) {
  const need = needsYou(downloads).map((d) => d.id);
  const out = new Set(need.filter((id) => seen.has(id) || opened));
  return out;
}

// fileFolders groups a torrent's files by their top folder, in order of first
// appearance: [{ name, items: [file indexes] }], name "" for files at the top.
// A torrent with no more than one such folder is not grouped (one entry, name "").
function fileFolders(paths) {
  const by = new Map();
  paths.forEach((p, i) => {
    const name = p.includes("/") ? p.slice(0, p.indexOf("/")) : "";
    if (!by.has(name)) by.set(name, []);
    by.get(name).push(i);
  });
  const named = [...by.keys()].filter(Boolean).length;
  return named < 2 ? [{ name: "", items: paths.map((_, i) => i) }] : [...by].map(([name, items]) => ({ name, items }));
}

// pickTotal words what is ticked: "3 of 240 files · 1.2 GB".
function pickTotal(sizes, ticked) {
  let n = 0, bytes = 0;
  sizes.forEach((s, i) => { if (ticked[i]) { n++; bytes += s; } });
  return `${n} of ${sizes.length} files · ${fmtBytes(bytes)}`;
}

if (typeof module !== "undefined") {
  module.exports = { fileFolders, pickTotal, addSubmits, linesNote, finishedVerdict, finishedSummary, badgeOf, seenAfter, addLines, addView, plainFtpNote, addFileCheck, matchesSearch, parseQuery, errorSource, logLines, announcements, fmtBytes, fmtSpeed, setSpeedUnit, speedUnitLabel, speedToUnit, speedFromUnit, fmtETA, percent, progressText, finished, groupView, extractView, predict, viaLinks, group, SECTIONS, SECTION_KEYS, tileOf, rowNums, mirrorRows, moveBefore, statusLine, problemLinks, seedLine, bulkActions, routerWarnings, routerGroups, lineResults, familyText, ipv6Hints };
}
