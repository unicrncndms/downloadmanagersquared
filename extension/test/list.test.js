"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { sortDownloads, moveBefore, statusLine, problemLinks, viaLine, seedLine, bulkActions, finished } = require("../lib/list.js");

const dl = (id, status, extra) => ({ kind: "http", id, status, size: 0, done: 0, links: {}, actions: {}, ...extra });

test("failed and waiting-for-you first, then downloading, seeding, queued, paused, finished; newest first in each", () => {
  // The engine lists oldest first.
  const list = [
    dl("done1", "completed"), dl("run1", "running"), dl("q", "queued"), dl("seed", "seeding", { kind: "torrent" }),
    dl("p", "paused"), dl("fail", "failed"), dl("run2", "running"), dl("done2", "completed"),
    dl("choose", "choosing", { kind: "torrent" }), dl("perr", "paused", { error: "Paused: the folder was removed" }),
    dl("ask", "running", { folder: { on: true } }), dl("check", "checking", { kind: "torrent" }),
  ];
  assert.deepEqual(sortDownloads(list).map((d) => d.id),
    ["ask", "perr", "choose", "fail", "check", "run2", "run1", "seed", "q", "p", "done2", "done1"]);
});

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

test("a running card says which interfaces it uses and how many connections each", () => {
  const names = { eth: "Ethernet", wifi: "Wi-Fi", vpn: "VPN" };
  const nameOf = (id) => names[id];
  const d = dl("a", "running", { links: {
    wifi: { conns: 2, speed: 4.4e6 }, eth: { conns: 4, speed: 7.1e6 }, vpn: { conns: 0, label: "Failed" },
  } });
  // Busiest first; one not connected isn't listed.
  assert.equal(viaLine(d, nameOf), "via Ethernet 4× · Wi-Fi 2×");
  const t = dl("t", "seeding", { kind: "torrent", links: { eth: { conns: 3 }, wifi: { conns: 1 } } });
  assert.equal(viaLine(t, nameOf), "via Ethernet 3 peers · Wi-Fi 1 peer");
  assert.equal(viaLine(dl("p", "paused", { links: { eth: { conns: 4 } } }), nameOf), "");
  assert.equal(viaLine(dl("q", "running", { links: {} }), nameOf), "");
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

test("finished is complete and not seeding", () => {
  assert.equal(finished(dl("a", "completed")), true);
  assert.equal(finished(dl("b", "seeding")), false);
  assert.equal(finished(dl("c", "running")), false);
});

test("queued downloads list in the order they will start, and move by drop or to an end", () => {
  const list = [dl("run", "running"), dl("q1", "queued"), dl("q2", "queued"), dl("q3", "queued")];
  assert.deepEqual(sortDownloads(list).map((d) => d.id), ["run", "q1", "q2", "q3"]);
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

test("a Metalink's files are drawn as one group where its first file sorts", () => {
  const { listEntries } = require("../lib/list.js");
  const sorted = [dl("a", "running", { group: "g", groupName: "discs" }), dl("x", "running"), dl("b", "queued", { group: "g" }), dl("c", "queued", { group: "h" })];
  const e = listEntries(sorted);
  assert.deepEqual(e.map((x) => x.d ? x.d.id : `${x.name}:${x.items.map((d) => d.id).join(",")}`), ["discs:a,b", "x", "c"]);
});

test("a group card adds its files up and offers what any file can do", () => {
  const { groupView } = require("../lib/list.js");
  const v = groupView([
    dl("a", "running", { size: 100e6, done: 50e6, speed: 2e6, label: "Downloading", actions: { pause: true }, partialFile: true }),
    dl("b", "completed", { size: 100e6, done: 100e6, label: "Completed" }),
  ]);
  assert.equal(v.left, "75% · 150 MB / 200 MB · 1 of 2 files done");
  assert.equal(v.right, "↓ 2.0 MB/s");
  assert.equal(v.label, "Downloading");
  assert.ok(v.pause && !v.resume && v.partialFile && !v.finished);
  const done = groupView([dl("a", "completed", { size: 1, done: 1 }), dl("b", "completed", { size: 1, done: 1 })]);
  assert.ok(done.finished && done.label === "Completed" && done.left.endsWith("· 2 files"));
});

test("a split archive's card counts its parts, says which is missing, and is done once extracted", () => {
  const { groupView } = require("../lib/list.js");
  const p = (id, status, set) => dl(id, status, { size: 100, done: status === "completed" ? 100 : 0, group: "parts:x.7z", label: "Completed",
    folder: { on: true, set } });
  const gap = { parts: 3, missing: [2], note: "Part 2 missing", files: 1, of: 4 };
  const v = groupView([p("a", "completed", gap), p("c", "completed", gap)]);
  assert.equal(v.left, "100% · 200 B / 200 B · 2 of 3 parts done · 1 of 4 files extracted");
  assert.ok(!v.finished && v.label === "Waiting" && v.tone === "warn");
  const busy = { parts: 2, files: 1, of: 4 };
  assert.equal(groupView([p("a", "completed", busy), p("b", "completed", busy)]).label, "Extracting");
  const fin = { parts: 2, files: 4, of: 4, finished: true };
  const done = groupView([p("a", "completed", fin), p("b", "completed", fin)]);
  assert.ok(done.finished && done.label === "Completed" && done.left.endsWith("· 2 parts"));
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
    { percent: 25, text: "Waiting for the download · 1m 30s left" });
  assert.equal(extractView([zip({})]), null); // not extracting (paused, say)
  assert.equal(extractView([dl("a", "running", { size: 1, done: 0 })]), null); // not a folder
  // A split archive: its Set's, once for all its parts.
  const part = (id, set) => dl(id, "completed", { size: 100, done: 100, folder: { on: true, into: "E:\\x", set } });
  const set = { parts: 3, extracted: 150, total: 300, phase: "Waiting for part 3", eta: 45 };
  assert.deepEqual(extractView([part("a", set), part("b", set)]), { percent: 50, text: "Waiting for part 3 · 45s left" });
  // A part missing says so, with no time left: it waits for you.
  assert.deepEqual(extractView([part("a", { ...set, note: "Part 2 missing" })]), { percent: 50, text: "Part 2 missing" });
  assert.equal(extractView([part("a", { ...set, finished: true })]), null);
});

test("a split archive's size is known before every part has started", () => {
  const { groupView } = require("../lib/list.js");
  const set = { parts: 3, total: 300, files: 0, of: 0 };
  const p = (id, st, size, done) => dl(id, st, { size, done, group: "parts:x.rar", folder: { on: true, into: "E:\\x", set } });
  const v = groupView([p("a", "running", 100, 60), p("b", "queued", -1, 0), p("c", "queued", -1, 0)]);
  assert.equal(v.percent, 20);
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
  assert.equal(s.active, 1); // the rest of the state as it was
  assert.equal(run.status, "running"); // the engine's state itself is not touched

  s = predict(state, "resume", { id: "p" });
  assert.deepEqual([s.downloads[3].status, s.downloads[3].error, s.downloads[3].actions], ["queued", "", { resume: false, pause: true }]);
  assert.equal(of(predict(state, "pauseAll")), "run:paused q1:paused q2:paused p:paused done:completed");
  assert.equal(of(predict(state, "resumeAll")), "run:running q1:queued q2:queued p:queued done:completed");
  assert.equal(of(predict(state, "remove", { id: "q1" })), "run:running q2:queued p:paused done:completed");
  assert.equal(predict(state, "setLimit", { id: "run", limit: 2e6 }).downloads[0].limit, 2e6);

  // The queue's order, and a slot taken from a download that is running.
  assert.equal(of(predict(state, "move", { id: "q2", before: "q1" })), "run:running q2:queued q1:queued p:paused done:completed");
  assert.equal(of(predict(state, "move", { id: "q1", before: "" })), "run:running q2:queued p:paused done:completed q1:queued");
  assert.equal(of(predict(state, "move", { id: "q2", before: "run" })), "q2:running run:queued q1:queued p:paused done:completed");

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

test("a row's section and its two numbers", () => {
  const { sectionName, rowNums } = require("../lib/list.js");
  const names = [dl("a", "running"), dl("b", "queued"), dl("c", "paused"), dl("d", "paused", { error: "no connection" }),
    dl("e", "completed"), dl("f", "seeding"), dl("g", "failed")].map(sectionName);
  assert.deepEqual(names, ["Downloading", "Queue", "Paused", "Needs you", "Finished", "Seeding", "Needs you"]);
  // A part of a split archive still being extracted is fetched, not finished.
  assert.notEqual(sectionName(dl("p", "completed", { folder: { on: true, set: { finished: false } } })), "Finished");

  assert.deepEqual(rowNums(dl("a", "running", { size: 1000, done: 390, speed: 2e6, eta: 65 })), { main: "2.0 MB/s", sub: "39% · 1m 05s left" });
  assert.deepEqual(rowNums(dl("c", "paused", { size: 2e9, done: 1e9 })), { main: "50%", sub: "1.0 GB / 2.0 GB" });
  assert.deepEqual(rowNums(dl("b", "queued", { size: 2e9, done: 0 })), { main: "2.0 GB", sub: "" });
  assert.deepEqual(rowNums(dl("e", "completed", { size: 2e9, done: 2e9 })), { main: "2.0 GB", sub: "" });
  assert.deepEqual(rowNums(dl("u", "paused", { size: -1, done: 5e6 })), { main: "5.0 MB", sub: "" });
});

test("the Add box offers what applies to the links in it", () => {
  const { addView } = require("../lib/list.js");
  assert.deepEqual(addView(""), { count: 0, bad: 0, page: false, vpn: false, label: "Add" });
  assert.deepEqual(addView("magnet:?xt=urn:btih:2da0"), { count: 1, bad: 0, page: false, vpn: true, label: "Add" });
  assert.deepEqual(addView("https://a.example/x.iso\nnot a link\n\nmagnet:?xt=urn:btih:1"), { count: 2, bad: 1, page: true, vpn: true, label: "Add 2 downloads" });
});

test("what waits for the user is in Needs you, however the download itself is doing", () => {
  const { sectionName } = require("../lib/list.js");
  const folder = (extra) => ({ on: true, into: "E:\\x", files: 0, of: 0, ...extra });
  assert.equal(sectionName(dl("p", "running", { folder: folder({ ask: "password" }) })), "Needs you");
  assert.equal(sectionName(dl("w", "completed", { folder: folder({ ask: "wrong", set: { finished: false } }) })), "Needs you");
  assert.equal(sectionName(dl("m", "running", { folder: folder({ set: { missing: [2], finished: false } }) })), "Needs you");
  assert.equal(sectionName(dl("r", "running", { links: { eth: { canRetry: true } } })), "Needs you");
  // A part fetched while the rest of its archive downloads and extracts is at work, not paused.
  assert.equal(sectionName(dl("x", "completed", { folder: folder({ set: { finished: false } }) })), "Downloading");
});

test("a row's status tile says its section: what needs you, at work, seeding, queued by number, resting, finished", () => {
  const { tileOf } = require("../lib/list.js");
  const kinds = [dl("a", "failed"), dl("b", "running"), dl("c", "seeding"), dl("d", "queued"), dl("e", "paused"), dl("f", "completed")]
    .map((d) => { const t = tileOf(d); return t.kind + ":" + t.icon; });
  assert.deepEqual(kinds, ["need:alert", "run:down", "seed:up", ":", ":pause", "done:check"]);
});

test("time left counts days past two days", () => {
  const { fmtETA } = require("../lib/list.js");
  assert.equal(fmtETA(3 * 3600 + 5 * 60), "3h 05m");
  assert.equal(fmtETA(47 * 3600), "47h 00m");
  assert.equal(fmtETA(987654), "11d 10h");
});
