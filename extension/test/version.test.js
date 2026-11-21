"use strict";

const test = require("node:test");
const assert = require("node:assert");
const { versionsClash, engineBehind, olderSide, updateNote, releaseNotesURL } = require("../lib/version.js");

test("before 1.0 the minor must match, patches never matter", () => {
  assert.equal(versionsClash("0.3.0", "0.3.5"), false);
  assert.equal(versionsClash("0.3.0", "0.5.0"), true);
  assert.equal(versionsClash("0.4.0", "0.3.9"), true);
});

test("an extension one minor newer than the engine is no clash, only behind", () => {
  assert.equal(versionsClash("0.12.1", "0.13.0"), false);
  assert.equal(engineBehind("0.12.1", "0.13.0"), true);
  assert.equal(engineBehind("1.2.0", "1.3.4"), true);
  assert.equal(engineBehind("0.13.0", "0.13.0"), false);
  assert.equal(engineBehind("0.13.0", "0.12.0"), false);
  assert.equal(engineBehind("0.12.0", "0.14.0"), false);
  assert.equal(engineBehind("0.9.0", "1.0.0"), false);
  assert.equal(versionsClash("0.9.0", "1.0.0"), true);
  assert.equal(engineBehind("dev", "0.13.0"), false);
});

test("the same clash cases as the engine's relver.Clash", () => {
  for (const [engine, ext, want] of require("../../engine/internal/relver/testdata/clash.json")) {
    assert.equal(versionsClash(engine, ext), want, `versionsClash(${engine}, ${ext})`);
  }
});

test("from 1.0 the major must match", () => {
  assert.equal(versionsClash("1.2.0", "1.9.0"), false);
  assert.equal(versionsClash("1.2.0", "2.0.0"), true);
  assert.equal(versionsClash("2.1.3", "1.9.9"), true);
});

test("development builds skip the check", () => {
  assert.equal(versionsClash("dev", "0.4.0"), false);
  assert.equal(versionsClash("0.3.0", "0.0.0"), false);
  assert.equal(versionsClash("", "1.0.0"), false);
  assert.equal(versionsClash(undefined, "1.0.0"), false);
});

test("the older side of a clash", () => {
  assert.equal(olderSide("0.7.0", "0.9.1"), "engine");
  assert.equal(olderSide("0.8.0", "0.7.9"), "extension");
  assert.equal(olderSide("1.0.0", "0.9.0"), "extension");
  assert.equal(olderSide("dev", "0.9.0"), "");
});

test("the update note: recorded silently once, shown after a change, then quiet", () => {
  assert.equal(updateNote("", "0.8.0"), "record");
  assert.equal(updateNote("0.7.0", "0.8.0"), "show");
  assert.equal(updateNote("0.8.0", "0.8.0"), "");
  assert.equal(updateNote("", "dev"), "");
});

test("release notes link", () => {
  const latest = "https://github.com/o/r/releases/latest";
  assert.equal(releaseNotesURL(latest, "0.8.0"), "https://github.com/o/r/releases/tag/v0.8.0");
});
