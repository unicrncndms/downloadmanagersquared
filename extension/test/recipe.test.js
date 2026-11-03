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

const { budget, replayTimeout, withMeasuredWaits, recorded } = require("../lib/recipe.js");

test("a step's budget is twice its recorded wait plus 15 s", () => {
  assert.equal(budget(0), 15000);
  assert.equal(budget(10000), 35000);
  assert.equal(budget(undefined), 15000); // recipes from before waits were recorded
});

test("replay's overall timeout is every budget plus a minute", () => {
  const recipe = { steps: [{ wait: 1000 }, { wait: 10000 }], downloadWait: 5000 };
  assert.equal(replayTimeout(recipe), 17000 + 35000 + 25000 + 60000);
  assert.equal(replayTimeout({ steps: [{}] }), 15000 + 15000 + 60000);
});

test("a successful replay replaces the waits with the measured ones", () => {
  const recipe = { steps: [{ text: "a", wait: 1000 }, { text: "b", wait: 10000 }], downloadWait: 5000, learned: 1 };
  const next = withMeasuredWaits(recipe, { steps: [800, undefined], download: 7000 });
  assert.deepEqual(next, { steps: [{ text: "a", wait: 800 }, { text: "b", wait: 10000 }], downloadWait: 7000, learned: 1 });
  assert.equal(recipe.steps[0].wait, 1000, "the old recipe is left alone");
});

test("a recording keeps the waits it saw", () => {
  const old = { steps: [{ text: "a", wait: 1000 }, { text: "old", wait: 3 }], downloadWait: 2 };
  const r = recorded(old, 1, [{ text: "new", wait: 4000 }], 6000, 99);
  assert.deepEqual(r, { steps: [{ text: "a", wait: 1000 }, { text: "new", wait: 4000 }], downloadWait: 6000, learned: 99 });
  assert.deepEqual(recorded(undefined, 0, [], 0, 5), { steps: [], downloadWait: 0, learned: 5 });
});
