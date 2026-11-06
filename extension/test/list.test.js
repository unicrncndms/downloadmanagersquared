"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { sortDownloads, statusLine, problemLinks, seedLine, bulkActions, finished } = require("../lib/list.js");

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

test("finished is complete and not seeding", () => {
  assert.equal(finished(dl("a", "completed")), true);
  assert.equal(finished(dl("b", "seeding")), false);
  assert.equal(finished(dl("c", "running")), false);
});
