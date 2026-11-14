"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { shortName, stripSpeed, unitField, sameFolder, canMoveFolder, browserNames } = require("../lib/uirules.js");
const { setSpeedUnit } = require("../lib/list.js");

test("shortName: first word, three letters when long", () => {
  assert.equal(shortName("Wi-Fi"), "Wi-Fi");
  assert.equal(shortName("Ethernet 2"), "Eth");
  assert.equal(shortName("Hotspot 1"), "Hot");
});

test("stripSpeed and unitField follow the unit", () => {
  setSpeedUnit("MBps");
  assert.equal(stripSpeed(4.4e6), "4.4");
  assert.equal(stripSpeed(87e6), "87.0");
  assert.equal(stripSpeed(120e6), "120");
  assert.equal(stripSpeed(1.2e9), "1.2G");
  assert.equal(unitField(0), "");
  assert.equal(unitField(1.5e6), 1.5);
  setSpeedUnit("Mbps");
  assert.equal(unitField(1e6), 8);
  setSpeedUnit("MBps");
});

test("sameFolder ignores case and empty", () => {
  assert.ok(sameFolder("C:\Down", "c:\down"));
  assert.ok(sameFolder("", undefined));
  assert.ok(!sameFolder("C:\a", "C:\b"));
});

test("canMoveFolder: only paused or queued, not torrent, not folder", () => {
  assert.ok(canMoveFolder({ kind: "http", status: "paused", actions: {} }));
  assert.ok(canMoveFolder({ kind: "http", status: "queued" }));
  assert.ok(!canMoveFolder({ kind: "http", status: "running" }));
  assert.ok(!canMoveFolder({ kind: "torrent", status: "paused" }));
  assert.ok(!canMoveFolder({ kind: "http", status: "paused", folder: { on: true } }));
  assert.ok(!canMoveFolder({ kind: "http", status: "completed", section: "done" }));
});

test("browserNames from exe paths", () => {
  const bs = String.fromCharCode(92);
  assert.deepEqual(browserNames(["C:" + bs + "App" + bs + "firefox.exe", "C:/Other/Firefox.EXE", "/z/chrome.exe"]), ["Firefox", "Chrome"]);
  assert.deepEqual(browserNames(undefined), []);
});

test("a square with written bytes is never drawn empty (partTiles)", () => {
  const { partTiles } = require("../lib/uirules.js");
  // Four squares: untouched, a tenth by connection b, nine tenths by a, whole.
  const t = partTiles("0-1b9a0-", 4);
  assert.equal(t[0], null);
  assert.deepEqual(t[1], { n: 1, who: 1 });
  assert.deepEqual(t[2], { n: 9, who: 0 });
  assert.equal(t[3], null);
  assert.deepEqual(partTiles("5-", 1), [{ n: 5, who: -1 }]);
  assert.deepEqual(partTiles(undefined, 2), [null, null]); // an engine that sends none
});

test("a pulsing front shows what is written of its square, so it matches the tile it leaves (frontFill)", () => {
  const { frontFill } = require("../lib/uirules.js");
  // The front is 90% through the square, but a chunk began inside it: only 5 tenths are on disk.
  assert.equal(frontFill("8", { n: 5, who: 0 }, true), 5 / 9);
  assert.equal(frontFill("8", null, true), 0);
  assert.equal(frontFill("3", null, false), 3 / 9); // an engine without mapPart
  assert.equal(frontFill(undefined, null, false), 0);
});
