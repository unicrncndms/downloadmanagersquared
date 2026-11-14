"use strict";

const test = require("node:test");
const assert = require("node:assert");
const { versionsClash, olderSide, updateNote, releaseNotesURL } = require("../lib/version.js");

test("before 1.0 the minor must match, patches never matter", () => {
  assert.equal(versionsClash("0.3.0", "0.3.5"), false);
  assert.equal(versionsClash("0.3.0", "0.4.0"), true);
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
  assert.equal(olderSide("0.7.0", "0.8.1"), "engine");
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
