"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { linesNote, finishedVerdict, moveBefore, statusLine, problemLinks, viaLinks, seedLine, bulkActions, addSubmits, finished, finishedSummary, badgeOf, seenAfter } = require("../lib/list.js");

// The engine's section for a status; a test of what needs the user says "needs".
const SECTION = { running: "active", verifying: "active", metadata: "active", checking: "active", seeding: "seeding", queued: "queued",
  paused: "paused", completed: "done", failed: "needs", choosing: "needs" };
const dl = (id, status, extra) => ({ kind: "http", id, status, section: SECTION[status], size: 0, done: 0, links: {}, actions: {}, ...extra });

test("the status line is short: percent and sizes, with speed and time left while running", () => {
  const d = dl("a", "running", { size: 42e9, done: 13e9, speed: 9.2e6, eta: 3150, note: "412 of 1,280 files extracted" });
  const s = statusLine(d);
  assert.equal(s.left, "31% · 13.0 GB / 42.0 GB");
  assert.equal(s.right, "↓ 9.2 MB/s · 52m 30s left");
  assert.equal(s.sub, "412 of 1,280 files extracted"); // the engine's note
  assert.equal(statusLine(dl("b", "paused", { size: 10e6, done: 5e6 })).right, "");
  // Size not known yet: what has arrived.
  assert.equal(statusLine(dl("c", "running", { size: -1, done: 3e6, speed: 1e6, eta: -1 })).left, "3.0 MB");
});

test("a Download as folder counts its files on the second line", () => {
  const d = dl("a", "running", { size: 42e9, done: 13e9, speed: 1, eta: 1, note: "412 of 1,280 files extracted", folder: { on: true, into: "E:\\x", files: 412, of: 1280 } });
  assert.equal(statusLine(d).sub, "412 / 1,280 files");
  const rar = dl("r", "running", { size: 42e9, done: 13e9, folder: { on: true, into: "E:\\x", files: 1203, of: 0 } });
  assert.equal(statusLine(rar).sub, "1,203 files extracted");
});

test("an error takes the status line's place", () => {
  const s = statusLine(dl("a", "paused", { size: 10, done: 5, error: "Paused: the folder was removed" }));
  assert.equal(s.error, "Paused: the folder was removed");
});

test("only interfaces with a problem show on the card", () => {
  const d = dl("a", "running", { links: {
    eth: { label: "Downloading", tone: "ok" },
    wifi: { label: "Server busy · 2 of 4 connections", tone: "warn" },
    vpn: { label: "Failed", tone: "bad", error: "connection refused", canRetry: true },
    idle: { label: "Idle", tone: "dim" },
    retry: { label: "Waiting", tone: "dim", canRetry: true },
  } });
  assert.deepEqual(problemLinks(d).sort(), ["retry", "vpn", "wifi"]);
  assert.deepEqual(problemLinks(dl("b", "completed", { links: { x: { tone: "bad" } } })), []);
});

test("seeding says how far it is to the ratio target", () => {
  const d = dl("c", "seeding", { kind: "torrent", upSpeed: 3e5, ratio: 0.15 });
  assert.equal(seedLine(d, { seedRatio: 1 }), "↑ 300 KB/s · ratio 0.15 / 1.00");
  assert.equal(seedLine(d, { seedRatio: 0 }), "↑ 300 KB/s · ratio 0.15");
  // A torrent still downloading: its upload and ratio only.
  assert.equal(seedLine(dl("t", "running", { kind: "torrent", upSpeed: 0, ratio: 0, uploaded: 0 }), {}), "");
  assert.equal(seedLine(dl("t", "running", { kind: "torrent", upSpeed: 2e4, ratio: 0.01, uploaded: 1e6 }), {}), "↑ 20.0 KB/s · ratio 0.01");
});

test("Pause all and Resume all show only when two or more downloads can use them", () => {
  const p = (id) => dl(id, "running", { actions: { pause: true } });
  const r = (id) => dl(id, "paused", { actions: { resume: true } });
  assert.deepEqual(bulkActions([p("a"), r("b")]), { pauseAll: false, resumeAll: false });
  assert.deepEqual(bulkActions([p("a"), p("b"), r("c")]), { pauseAll: true, resumeAll: false });
  assert.deepEqual(bulkActions([r("a"), r("b")]), { pauseAll: false, resumeAll: true });
});

test("finished is the engine's done section: complete and not seeding", () => {
  assert.equal(finished(dl("a", "completed")), true);
  assert.equal(finished(dl("p", "completed", { section: "active" })), false); // a part whose archive isn't extracted
  assert.equal(finished(dl("b", "seeding")), false);
  assert.equal(finished(dl("c", "running")), false);
});

test("queued downloads move by drop or to an end", () => {
  const list = [dl("run", "running"), dl("q1", "queued"), dl("q2", "queued"), dl("q3", "queued")];
  assert.equal(moveBefore(list, "q3", "q1"), "q1"); // dragged up: takes q1's place
  assert.equal(moveBefore(list, "q1", "q2"), "q3"); // dragged down: goes after q2
  assert.equal(moveBefore(list, "q1", "q3"), ""); // after the last one
  assert.equal(moveBefore(list, "q3", "first"), "q1");
  assert.equal(moveBefore(list, "q1", "last"), "");
  assert.equal(moveBefore(list, "q1", "first"), null); // already there
  assert.equal(moveBefore(list, "q3", "last"), null);
  assert.equal(moveBefore(list, "q2", "up"), "q1"); // the arrows: one place
  assert.equal(moveBefore(list, "q1", "down"), "q3");
  assert.equal(moveBefore(list, "q2", "down"), "");
  assert.equal(moveBefore(list, "q1", "up"), null);
  assert.equal(moveBefore(list, "q3", "down"), null);
  assert.equal(moveBefore(list, "run", "q1"), null); // only queued ones move
  assert.equal(moveBefore(list, "q1", "run"), "run"); // dropped on a running one: takes its slot
  assert.equal(moveBefore(list, "q1", "done"), null);
});

test("interfaces behind one router get one warning naming them all; tunnels and others none", () => {
  const { routerWarnings } = require("../lib/list.js");
  assert.deepEqual(routerWarnings([
    { id: "eth", name: "Ethernet", sameRouter: ["Wi-Fi"] },
    { id: "wifi", name: "Wi-Fi", sameRouter: ["Ethernet"] },
    { id: "usb", name: "Phone" },
    { id: "eth~vpn", name: "VPN via Ethernet", tunnel: true, sameRouter: ["x"] },
  ]), ["Ethernet and Wi-Fi go through the same router: together they are no faster than one of them."]);
  assert.deepEqual(routerWarnings([{ id: "a", name: "A" }]), []);
});

test("Check lines lists each public IP and warns about a shared one", () => {
  const { lineResults } = require("../lib/list.js");
  const r = lineResults([
    { id: "eth", name: "Ethernet", ip: "203.0.113.7" },
    { id: "mesh", name: "Mesh", ip: "203.0.113.7" },
    { id: "lte", name: "Phone", ip: "198.51.100.2" },
    { id: "x", name: "Old", error: "no answer from the STUN server" },
  ]);
  assert.deepEqual(r.lines, ["Ethernet: 203.0.113.7", "Mesh: 203.0.113.7", "Phone: 198.51.100.2", "Old: no answer (no answer from the STUN server)"]);
  assert.deepEqual(r.warnings, ["Ethernet and Mesh reach the internet from the same public IP (203.0.113.7): they share one uplink, so together they are no faster than one."]);
});

// A group summary as the engine sends it.
const summary = (extra) => ({ id: "g", name: "discs", parts: false, count: 2, items: 2, done: 0, size: 200e6, got: 0, speed: 0, finished: false,
  label: "Downloading", tone: "", pause: false, resume: false, partialFile: false, order: [], ...extra });

test("a group card words the engine's summary", () => {
  const { groupView } = require("../lib/list.js");
  const v = groupView(summary({ done: 1, got: 150e6, speed: 2e6, eta: -1, pause: true, partialFile: true }));
  assert.equal(v.left, "75% · 150 MB / 200 MB · 1 of 2 files done");
  // With the engine's time left for all of it, the line ends with that.
  assert.match(groupView(summary({ got: 150e6, speed: 2e6, eta: 25 })).left, / · .+ left$/);
  assert.equal(v.right, "↓ 2.0 MB/s");
  assert.equal(v.label, "Downloading");
  assert.ok(v.pause && !v.resume && v.partialFile && !v.finished);
  const done = groupView(summary({ done: 2, got: 200e6, finished: true, label: "Completed", tone: "ok" }));
  assert.ok(done.finished && done.label === "Completed" && done.tone === "ok" && done.left.endsWith("· 2 files") && done.right === "");
});

test("a split archive's card counts its parts, and its extraction has a line of its own", () => {
  const { groupView, extractView } = require("../lib/list.js");
  const v = groupView(summary({ parts: true, count: 3, items: 2, done: 2, size: 200, got: 200, label: "Waiting", tone: "warn", files: 1, of: 4 }));
  assert.equal(v.left, "100% · 200 B / 200 B · 2 of 3 parts done");
  const part = (set) => ({ kind: "", folder: { on: true, into: "E:/x", set } });
  const x = extractView([part({ phase: "Extracting", extracted: 50, total: 200, eta: 0, files: 1, of: 4 })]);
  assert.equal(x.text, "Extraction · 25% · 1 of 4 files");
  assert.equal(extractView([part({ phase: "Waiting for part 3", extracted: 50, total: 200, eta: 0 })]).text, "Extraction · Waiting for part 3");
  assert.ok(!v.finished && v.label === "Waiting" && v.tone === "warn");
  // The size isn't known before every part has started: only what is fetched.
  assert.equal(groupView(summary({ parts: true, count: 3, size: -1, got: 60 })).percent, 0);
  assert.equal(groupView(summary({ parts: true, count: 3, size: 300, got: 60 })).percent, 20);
});

test("mirrors show who uses them and why one was dropped", () => {
  const { mirrorRows } = require("../lib/list.js");
  const d = dl("a", "running", {
    mirrors: [{ url: "https://a.example/f.iso" }, { url: "https://b.example/f.iso", dropped: "different file size" }, { url: "https://c.example/f.iso" }],
    links: { eth: { mirror: "a.example" }, wifi: { mirror: "a.example" } },
  });
  assert.deepEqual(mirrorRows(d, (id) => id.toUpperCase()), [
    { host: "a.example", url: "https://a.example/f.iso", users: ["ETH", "WIFI"], dropped: "" },
    { host: "b.example", url: "https://b.example/f.iso", users: [], dropped: "different file size" },
    { host: "c.example", url: "https://c.example/f.iso", users: [], dropped: "" },
  ]);
});

test("Add download takes one link per line, skipping blanks and spaces and reporting bad lines by number", () => {
  const { addLines } = require("../lib/list.js");
  assert.deepEqual(addLines("https://a.example/1.zip"), { urls: [{ line: 1, url: "https://a.example/1.zip" }], bad: [] });
  assert.deepEqual(addLines("  https://a.example/1\r\n\n   \nnot a link\nmagnet:?xt=urn:btih:abc  \n"), {
    urls: [{ line: 1, url: "https://a.example/1" }, { line: 5, url: "magnet:?xt=urn:btih:abc" }],
    bad: [{ line: 4, url: "not a link" }],
  });
  assert.deepEqual(addLines(""), { urls: [], bad: [] });
});

test("the search box matches file name, host and status, in any case", () => {
  const { matchesSearch } = require("../lib/list.js");
  const d = dl("a", "running", { filename: "Ubuntu-24.04.iso", url: "https://releases.example.org/x?token=secret", label: "Downloading" });
  for (const q of ["", "  ", "ubuntu", "ISO", "releases.example", "download"]) assert.ok(matchesSearch(d, q), q);
  for (const q of ["fedora", "token", "secret"]) assert.ok(!matchesSearch(d, q), q); // the query string isn't searched
  assert.ok(matchesSearch(dl("m", "queued", { url: "magnet:?xt=urn:btih:abc", groupName: "Debian DVDs" }), "debian"));
});

test("the activity log reads newest first, with local time and the connection's name", () => {
  const { logLines } = require("../lib/list.js");
  const at = (h) => new Date(2026, 10, 7, h, 5, 9).toISOString();
  const lines = logLines([
    { time: at(9), text: "Queued" },
    { time: at(10), iface: "wifi", text: "Link expired: HTTP 403" },
  ], (id) => ({ wifi: "Wi-Fi" })[id]);
  assert.deepEqual(lines, ["2026-11-07 10:05:09 · Wi-Fi · Link expired: HTTP 403", "2026-11-07 09:05:09 · Queued"]);
  assert.deepEqual(logLines(null, () => ""), []);
});

test("a screen reader hears a download finish or fail once, and nothing about downloads already there", () => {
  const { announcements } = require("../lib/list.js");
  let r = announcements(null, [dl("a", "running", { filename: "a.iso" }), dl("b", "failed", { filename: "b.iso" })]);
  assert.deepEqual(r.texts, []); // just opened: old news stays quiet
  r = announcements(r.seen, [dl("a", "completed", { filename: "a.iso" }), dl("b", "failed", { filename: "b.iso" }), dl("c", "running")]);
  assert.deepEqual(r.texts, ["a.iso finished"]);
  r = announcements(r.seen, [dl("a", "completed", { filename: "a.iso" }), dl("c", "failed", { url: "https://x/c", error: "disk full" })]);
  assert.deepEqual(r.texts, ["https://x/c failed: disk full"]);
  r = announcements(r.seen, [dl("t", "running", { kind: "torrent", filename: "t" })]);
  r = announcements(r.seen, [dl("t", "seeding", { kind: "torrent", filename: "t" })]);
  assert.deepEqual(r.texts, ["t finished"]);
});

test("a torrent that finishes seeding isn't announced a second time", () => {
  const { announcements } = require("../lib/list.js");
  let r = announcements(new Map([["t", "seeding"]]), [dl("t", "completed", { kind: "torrent", filename: "t" })]);
  assert.deepEqual(r.texts, []);
});

test("each connection's badge names its families; IPv6-only adapters left out get a hint", () => {
  const { familyText, ipv6Hints } = require("../lib/list.js");
  assert.equal(familyText({ families: ["IPv4"] }), "IPv4");
  assert.equal(familyText({ families: ["IPv6"] }), "IPv6");
  assert.equal(familyText({ families: ["IPv4", "IPv6"] }), "IPv4 + IPv6");
  assert.equal(familyText({}), ""); // an older engine: no badge
  assert.deepEqual(ipv6Hints({ ipv6Only: ["Phone"] }), ["Phone has only IPv6: turn on Enable IPv6 (experimental) to use it."]);
  assert.deepEqual(ipv6Hints({}), []);
});

test("Check lines compares IPv6-only lines by their /64", () => {
  const { lineResults } = require("../lib/list.js");
  const r = lineResults([
    { id: "usb", name: "Phone", ip: "2001:470:1:2::6", line: "2001:470:1:2::/64" },
    { id: "wifi", name: "Hotspot", ip: "2001:470:1:2::7", line: "2001:470:1:2::/64" },
    { id: "lte", name: "Stick", ip: "2001:470:9:9::8", line: "2001:470:9:9::/64" },
    { id: "eth", name: "Ethernet", ip: "203.0.113.7", line: "203.0.113.7" },
  ]);
  assert.deepEqual(r.warnings, ["Phone and Hotspot reach the internet from the same IPv6 network (2001:470:1:2::/64): they share one uplink, so together they are no faster than one."]);
});

test("the extraction has its own bar: how far, what it does or waits for, the time left", () => {
  const { extractView } = require("../lib/list.js");
  const zip = (folder) => dl("z", "running", { size: 1000, done: 600, folder: { on: true, into: "E:\\x", ...folder } });
  assert.deepEqual(extractView([zip({ extracted: 250, phase: "Waiting for the download", extractEta: 90 })]),
    { percent: 25, text: "Extraction · Waiting for the download · 1m 30s left" });
  assert.equal(extractView([zip({})]), null); // not extracting (paused, say)
  assert.equal(extractView([dl("a", "running", { size: 1, done: 0 })]), null); // not a folder
  // A split archive: its Set's, once for all its parts.
  const part = (id, set) => dl(id, "completed", { size: 100, done: 100, folder: { on: true, into: "E:\\x", set } });
  const set = { parts: 3, extracted: 150, total: 300, phase: "Waiting for part 3", eta: 45 };
  assert.deepEqual(extractView([part("a", set), part("b", set)]), { percent: 50, text: "Extraction · Waiting for part 3 · 45s left" });
  // The group's time left counts the download too: the later one wins.
  assert.equal(extractView([part("a", set)], 90).text, "Extraction · Waiting for part 3 · 1m 30s left");
  // A part missing says so, with no time left: it waits for you.
  assert.deepEqual(extractView([part("a", { ...set, note: "Part 2 missing" })]), { percent: 50, text: "Extraction · Part 2 missing" });
  assert.equal(extractView([part("a", { ...set, finished: true })]), null);
});

test("a click shows at once: what a request will leave of the engine's state", () => {
  const { predict } = require("../lib/list.js");
  const run = dl("run", "running", { speed: 5, label: "Downloading", actions: { pause: true } });
  const q1 = dl("q1", "queued", { actions: { pause: true } }), q2 = dl("q2", "queued", { actions: { pause: true } });
  const stopped = dl("p", "paused", { error: "Paused: no connection", actions: { resume: true } });
  const done = dl("done", "completed");
  const state = { active: 1, downloads: [run, q1, q2, stopped, done] };
  const of = (s) => s.downloads.map((d) => `${d.id}:${d.status}`).join(" ");

  let s = predict(state, "pause", { id: "run" });
  assert.equal(of(s), "run:paused q1:queued q2:queued p:paused done:completed");
  assert.deepEqual([s.downloads[0].label, s.downloads[0].speed, s.downloads[0].actions], ["Paused", 0, { pause: false, resume: true }]);
  assert.equal(s.downloads[0].section, "paused"); // the list sorts by section
  assert.equal(s.active, 1); // the rest of the state as it was
  assert.equal(run.status, "running"); // the engine's state itself is not touched

  s = predict(state, "resume", { id: "p" });
  assert.deepEqual([s.downloads[3].status, s.downloads[3].error, s.downloads[3].actions], ["queued", "", { resume: false, pause: true }]);
  assert.equal(of(predict(state, "pauseAll")), "run:paused q1:paused q2:paused p:paused done:completed");
  assert.equal(of(predict(state, "resumeAll")), "run:running q1:queued q2:queued p:queued done:completed");
  assert.equal(of(predict(state, "removeMany", { ids: ["run", "p", "nope"] })), "q1:queued q2:queued done:completed");
  assert.equal(of(predict(state, "remove", { id: "q1" })), "run:running q2:queued p:paused done:completed");
  assert.equal(predict(state, "setLimit", { id: "run", limit: 2e6 }).downloads[0].limit, 2e6);

  // The queue's order, and a slot taken from a download that is running.
  assert.equal(of(predict(state, "move", { id: "q2", before: "q1" })), "run:running q2:queued q1:queued p:paused done:completed");
  assert.equal(of(predict(state, "move", { id: "q1", before: "" })), "run:running q2:queued p:paused done:completed q1:queued");
  const swapped = predict(state, "move", { id: "q2", before: "run" });
  assert.equal(of(swapped), "q2:running run:queued q1:queued p:paused done:completed");
  assert.deepEqual(swapped.downloads.slice(0, 2).map((d) => d.section), ["active", "queued"]);

  // Download as folder: one download, or every part of a split archive.
  const part = (id) => dl(id, "queued", { group: "parts:x.rar", folder: { on: false } });
  const zip = dl("z", "running", { folder: { on: true, into: "" } });
  const fs = { downloads: [part("a"), part("b"), zip] };
  assert.deepEqual(predict(fs, "setFolder", { id: "a", on: true }).downloads.map((d) => d.folder.on), [true, true, true]);
  assert.deepEqual(predict(fs, "setFolder", { id: "z", into: "E:\\x" }).downloads.map((d) => d.folder.into), [undefined, undefined, "E:\\x"]);

  // Nothing to show early: the same state, so nothing is kept waiting.
  for (const [m, p] of [["pause", { id: "done" }], ["pause", { id: "gone" }], ["resume", { id: "run" }], ["showInFolder", { id: "run" }], ["setFolder", { id: "run", on: true }]]) {
    assert.equal(predict(state, m, p), state, m);
  }
});

test("a row's two numbers", () => {
  const { rowNums } = require("../lib/list.js");
  assert.deepEqual(rowNums(dl("a", "running", { size: 1000, done: 390, speed: 2e6, eta: 65 })), { main: "2.0 MB/s", sub: "39% · 1m 05s left" });
  assert.deepEqual(rowNums(dl("c", "paused", { size: 2e9, done: 1e9 })), { main: "50%", sub: "1.0 GB / 2.0 GB" });
  assert.deepEqual(rowNums(dl("b", "queued", { size: 2e9, done: 0 })), { main: "2.0 GB", sub: "" });
  assert.deepEqual(rowNums(dl("e", "completed", { size: 2e9, done: 2e9 })), { main: "2.0 GB", sub: "" });
  assert.deepEqual(rowNums(dl("u", "paused", { size: -1, done: 5e6 })), { main: "5.0 MB", sub: "" });
});

test("the arrows go round a menu's items, Home and End to its ends", () => {
  const { stepIndex } = require("../lib/list.js");
  assert.equal(stepIndex(0, 3, "ArrowDown"), 1);
  assert.equal(stepIndex(2, 3, "ArrowDown"), 0);
  assert.equal(stepIndex(0, 3, "ArrowUp"), 2);
  assert.equal(stepIndex(-1, 3, "ArrowDown"), 0);
  assert.equal(stepIndex(-1, 3, "ArrowUp"), 2);
  assert.equal(stepIndex(1, 3, "Home"), 0);
  assert.equal(stepIndex(1, 3, "End"), 2);
  assert.equal(stepIndex(1, 3, "a"), -1);
  assert.equal(stepIndex(-1, 0, "ArrowDown"), -1);
});

test("a login typed in the Add box goes into an ftp or sftp link, escaped", () => {
  const { withLogin } = require("../lib/list.js");
  assert.equal(withLogin("ftp://h.example/a.iso", "bob", "p@ss:w/rd"), "ftp://bob:p%40ss%3Aw%2Frd@h.example/a.iso");
  assert.equal(withLogin("sftp://h.example:2222/a.iso", "bob", ""), "sftp://bob@h.example:2222/a.iso");
  assert.equal(withLogin("ftps://old:pw@h.example/a.iso", "", "new"), "ftps://old:new@h.example/a.iso");
  assert.equal(withLogin("ftp://h.example/a.iso", "", ""), "ftp://h.example/a.iso");
  assert.equal(withLogin("https://h.example/a.iso", "bob", "pw"), "https://h.example/a.iso");
});

test("the Add box offers what applies to the links in it", () => {
  const { addView } = require("../lib/list.js");
  const { plainFtpNote } = require("../lib/list.js");
  assert.match(plainFtpNote("ftp://bob:pw@h.example/a.iso"), /unencrypted/);
  assert.equal(plainFtpNote("ftp://h.example/a.iso\nftps://bob:pw@h.example/a.iso\nhttps://u:p@x/y"), "");
  assert.deepEqual(addView(""), { count: 0, bad: 0, page: false, vpn: false, label: "Add" });
  assert.deepEqual(addView("magnet:?xt=urn:btih:2da0"), { count: 1, bad: 0, page: false, vpn: true, label: "Add" });
  assert.deepEqual(addView("https://a.example/x.iso\nnot a link\n\nmagnet:?xt=urn:btih:1"), { count: 2, bad: 1, page: true, vpn: true, label: "Add 2 downloads" });
});

test("a row's status tile says its section: what needs you, at work, seeding, queued by number, resting, finished", () => {
  const { tileOf } = require("../lib/list.js");
  const kinds = [dl("a", "failed"), dl("b", "running"), dl("c", "seeding"), dl("d", "queued"), dl("e", "paused"), dl("f", "completed")]
    .map((d) => { const t = tileOf(d); return t.kind + ":" + t.icon; });
  assert.deepEqual(kinds, ["need:alert", "run:down", "seed:up", ":", ":pause", "done:check"]);
});

test("a torrent's file step is a neutral pick tile, not an alert", () => {
  const { tileOf } = require("../lib/list.js");
  assert.deepEqual(tileOf(dl("a", "choosing", { section: "needs" })), { kind: "pick", icon: "folder" });
});

test("torrent files group by top folder only when there are several, and the ticked total is worded", () => {
  const { fileFolders, pickTotal } = require("../lib/list.js");
  assert.deepEqual(fileFolders(["a.txt", "d/b", "d/c"]), [{ name: "", items: [0, 1, 2] }]);
  assert.deepEqual(fileFolders(["a.txt", "d/b", "e/c", "d/f"]), [{ name: "", items: [0] }, { name: "d", items: [1, 3] }, { name: "e", items: [2] }]);
  assert.equal(pickTotal([1024, 2048, 4096], [true, false, true]), "2 of 3 files · 5.1 KB");
});

test("time left counts days past two days", () => {
  const { fmtETA } = require("../lib/list.js");
  assert.equal(fmtETA(3 * 3600 + 5 * 60), "3h 05m");
  assert.equal(fmtETA(47 * 3600), "47h 00m");
  assert.equal(fmtETA(987654), "11d 10h");
});

test("the tiles of a download's connections keep their places as the counts change", () => {
  const order = ["wifi", "eth", "wifi~vpn"];
  const at = (a, b) => dl("x", "running", { links: { eth: { conns: a }, wifi: { conns: b }, "wifi~vpn": { conns: 2 } } });
  assert.deepEqual(viaLinks(at(4, 1), order), ["wifi", "eth", "wifi~vpn"]);
  assert.deepEqual(viaLinks(at(1, 4), order), ["wifi", "eth", "wifi~vpn"]);
});

test("finishedSummary: size, time, speed and each connection's share when several fetched", () => {
  const nameOf = (id) => ({ eth: "Ethernet", wifi: "Wi-Fi" })[id];
  const base = { size: 3e9, added: "2026-11-03T10:00:00Z", finished: "2026-11-03T19:04:10Z", active: 250 }; // queued for hours: only the 250 s it ran count
  assert.equal(finishedSummary({ ...base, links: { eth: { bytes: 1.86e9 }, wifi: { bytes: 1.14e9 } } }, nameOf),
    "3.0 GB in 4m 10s · 12.0 MB/s · Ethernet 62%, Wi-Fi 38%");
  assert.equal(finishedSummary({ ...base, links: { eth: { bytes: 3e9 }, wifi: {} } }, nameOf), "3.0 GB in 4m 10s · 12.0 MB/s");
  assert.equal(finishedSummary({ size: 5, links: {} }, nameOf), "5 B");
  assert.equal(finishedSummary({ size: 5, added: "2026-11-03T19:00:00Z", finished: "2026-11-03T19:04:10Z", links: {} }, nameOf), "5 B"); // no active time recorded: no time or speed
});

test("badgeOf: ! for a download that needs you or failed, else the running count", () => {
  assert.deepEqual(badgeOf([]), { text: "", color: "#2563eb" });
  assert.equal(badgeOf([{ status: "running" }, { status: "running" }]).text, "2");
  assert.equal(badgeOf([{ status: "running" }, { status: "failed", section: "needs" }]).text, "!");
});

test("badgeOf: a failure the list has shown no longer keeps the !, a new one does", () => {
  const failed = { id: "old", status: "failed", section: "needs" };
  const running = { id: "r", status: "running" };
  let seen = seenAfter([failed, running], new Set(), false);
  assert.equal(badgeOf([failed, running], seen).text, "!");
  seen = seenAfter([failed, running], seen, true);
  assert.deepEqual(badgeOf([failed, running], seen), { text: "1", color: "#2563eb" });
  const fresh = { id: "new", status: "failed", section: "needs" };
  assert.equal(badgeOf([failed, fresh], seen).text, "!");
  // It runs again: its seen mark goes, so a later failure shows again.
  seen = seenAfter([{ ...failed, status: "running", section: "" }], seen, false);
  assert.equal(seen.has("old"), false);
  assert.equal(badgeOf([failed], seen).text, "!");
});

test("fmtSpeed, in bytes or bits, and the limit conversions", () => {
  const { fmtSpeed, setSpeedUnit, speedUnitLabel, speedToUnit, speedFromUnit } = require("../lib/list.js");
  assert.equal(fmtSpeed(1.5e6, "MBps"), "1.5 MB/s");
  assert.equal(fmtSpeed(0, "MBps"), "0 B/s");
  assert.equal(fmtSpeed(1.5e6, "Mbps"), "12.0 Mbit/s");
  assert.equal(fmtSpeed(125e6, "Mbps"), "1.0 Gbit/s");
  assert.equal(fmtSpeed(50, "Mbps"), "400 bit/s");
  assert.equal(fmtSpeed(0, "Mbps"), "0 bit/s");
  assert.equal(speedToUnit(1.25e6, "Mbps"), 10);
  assert.equal(speedToUnit(1.25e6, "MBps"), 1.25);
  assert.equal(speedFromUnit(10, "Mbps"), 1.25e6);
  assert.equal(speedFromUnit(1.25, "MBps"), 1.25e6);
  assert.equal(speedUnitLabel("Mbps"), "Mbit/s");
  setSpeedUnit("Mbps");
  assert.equal(fmtSpeed(1e6), "8.0 Mbit/s");
  setSpeedUnit("junk");
  assert.equal(fmtSpeed(1e6), "1.0 MB/s");
});

test("linesNote: one line hints a second, shared router promises no speed", () => {
  const w = { id: "wifi", name: "Wi-Fi" };
  assert.equal(linesNote([w]).add, true);
  const sep = linesNote([w, { id: "eth", name: "Ethernet" }]);
  assert.match(sep.text, /look separate/);
  assert.equal(sep.check, true);
  assert.equal(sep.shared, false);
  const same = linesNote([{ ...w, sameRouter: ["Ethernet"] }, { id: "eth", name: "Ethernet", sameRouter: ["Wi-Fi"] }]);
  assert.match(same.text, /Ethernet and Wi-Fi share one line and will not add speed/);
  assert.equal(same.shared, true);
  assert.equal(linesNote([w]).shared, false);
});

test("finishedVerdict: smallest share added, shared router no gain, nothing when unknown", () => {
  const nameOf = (id) => ({ eth: "Ethernet", wifi: "Wi-Fi" })[id];
  const d = { links: { eth: { bytes: 62 }, wifi: { bytes: 38 } } };
  assert.equal(finishedVerdict(d, [], nameOf), "Wi-Fi added 38%");
  assert.equal(finishedVerdict(d, [{ id: "eth", sameRouter: ["Wi-Fi"] }], nameOf), "Ethernet and Wi-Fi share one line: no gain");
  assert.equal(finishedVerdict({ links: { eth: { bytes: 5 } } }, [], nameOf), "");
});

test("Enter adds from a one-line Add box, Ctrl+Enter from any", () => {
  const key = (o) => ({ key: "Enter", ...o });
  assert.equal(addSubmits(key({}), "https://a.test/x"), true);
  assert.equal(addSubmits(key({}), "https://a.test/x\nhttps://a.test/y"), false);
  assert.equal(addSubmits(key({ ctrlKey: true }), "https://a.test/x\nhttps://a.test/y"), true);
  assert.equal(addSubmits(key({ shiftKey: true }), "https://a.test/x"), false);
  assert.equal(addSubmits({ key: "a" }, "x"), false);
});

test("is:failed, is:paused, is:queued, is:running and is:done filter by state", () => {
  const { matchesSearch } = require("../lib/list.js");
  const failed = dl("f", "failed", { filename: "a.iso", section: "needs" });
  const done = dl("d", "completed", { filename: "b.iso", section: "done" });
  const run = dl("r", "running", { filename: "c.iso", section: "active" });
  assert.ok(matchesSearch(failed, "is:failed") && !matchesSearch(done, "is:failed") && !matchesSearch(run, "IS:FAILED"));
  assert.ok(matchesSearch(done, "is:done") && matchesSearch(run, "is:running"));
  assert.ok(matchesSearch(failed, "is:failed is:done") && matchesSearch(done, "is:failed is:done")); // any of them
  assert.ok(matchesSearch(failed, "is:failed a.iso") && !matchesSearch(failed, "is:failed b.iso")); // with words, both
  assert.ok(matchesSearch(done, "is:nonsense") === false && matchesSearch(dl("x", "queued", { filename: "is:nonsense" }), "is:nonsense")); // not a state: plain text
});

test("a row's error leads with the connection it belongs to", () => {
  const { errorSource } = require("../lib/list.js");
  const links = { a: { error: "" }, b: { error: "the server answered 403" } };
  assert.equal(errorSource({ error: "the server answered 403", links }), "b");
  assert.equal(errorSource({ error: "Failed: all links", links }), "b"); // the only one with an error
  assert.equal(errorSource({ error: "x", links: { a: { error: "p" }, b: { error: "q" } } }), "");
  assert.equal(errorSource({ error: "x" }), "");
});

test("addFileCheck takes small torrent and Metalink files only", () => {
  const { addFileCheck } = require("../lib/list.js");
  assert.equal(addFileCheck("a.TORRENT", 5000), "");
  assert.equal(addFileCheck("a.meta4", 700000), "");
  assert.match(addFileCheck("a.meta4", 700001), /too big/);
  assert.match(addFileCheck("a.zip", 10), /only \.torrent/);
});

test("a running download held by a limit says so; Start now runs a queued one", () => {
  const { statusLine, predict } = require("../lib/list.js");
  assert.match(statusLine({ status: "running", speed: 5e6, eta: 10, size: 10, done: 1, limitedBy: "Wi-Fi's limit", limitedTo: 5e6 }).sub, /^limited to .* by Wi-Fi's limit$/);
  const s = { downloads: [{ id: "a", status: "queued" }] };
  assert.equal(predict(s, "startNow", { id: "a" }).downloads[0].status, "running");
});

test("a search word naming an inherited property is not a state word", () => {
  const { matchesSearch } = require("../lib/list.js");
  const d = { filename: "other.zip", status: "running", section: "active" };
  assert.equal(matchesSearch(d, "is:constructor"), false); // plain text now, not a filter that passes everything
  assert.equal(matchesSearch(d, "is:toString"), false);
  assert.equal(matchesSearch(d, "is:running"), true);
});

test("progress reads the same everywhere: done / size", () => {
  const { progressText, statusLine } = require("../lib/list.js");
  assert.equal(statusLine({ size: 2e6, done: 1e6, status: "paused" }).left, "50% · " + progressText(1e6, 2e6));
});

// The same samples as TestHumanMatchesTheListsRule in the engine's download package.
test("fmtBytes: base 1000, a decimal under 100", () => {
  const { fmtBytes } = require("../lib/list.js");
  for (const [n, want] of [[0, "0 B"], [999, "999 B"], [1500, "1.5 KB"], [150e6, "150 MB"], [3435973837, "3.4 GB"], [-1, "?"]]) {
    assert.equal(fmtBytes(n), want);
  }
});

test("parseQuery gives the same answers as the string, states apart from words", () => {
  const { matchesSearch, parseQuery } = require("../lib/list.js");
  assert.deepEqual(parseQuery("  Is:Failed ISO is:nope "), { states: ["failed"], q: "iso is:nope" });
  const d = dl("a", "failed", { filename: "a.iso", url: "https://host.example/a.iso" });
  for (const q of ["", "is:failed", "is:done", "host.example", "a.iso is:failed", "zzz"]) assert.equal(matchesSearch(d, parseQuery(q)), matchesSearch(d, q), q);
  assert.equal(matchesSearch(dl("m", "queued", { url: "magnet:?xt=urn:btih:abc" }), parseQuery("magnet")), false); // a magnet link has no host
});
