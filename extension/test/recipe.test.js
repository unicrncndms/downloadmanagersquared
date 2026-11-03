"use strict";

const test = require("node:test");
const assert = require("node:assert");
const { mergeSteps } = require("../lib/recipe.js");

test("a recording replaces the steps from where the user took over", () => {
  const old = [{ text: "Free download" }, { text: "Continue" }, { text: "Old button" }];
  assert.deepEqual(mergeSteps(old, 2, [{ text: "New button" }]), [{ text: "Free download" }, { text: "Continue" }, { text: "New button" }]);
  assert.deepEqual(mergeSteps(undefined, 0, [{ text: "Go" }]), [{ text: "Go" }]);
  assert.deepEqual(mergeSteps(old, 0, []), []);
});
