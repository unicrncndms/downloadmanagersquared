"use strict";

const test = require("node:test");
const assert = require("node:assert");
const { versionsClash } = require("../lib/version.js");

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
