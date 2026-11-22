"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { clickRows, undoRemoval } = require("../lib/selection.js");

// The list's rows in order: a group card is one row of several ids.
const ORDER = [["a"], ["b"], ["g1", "g2"], ["c"], ["d"]];
const ids = (sel) => [...sel].sort();

test("a click selects only that row, and a second click on it clears", () => {
  const sel = new Set(["a", "c"]);
  let anchor = clickRows(sel, "", ["b"], {}, ORDER);
  assert.deepEqual(ids(sel), ["b"]);
  assert.equal(anchor, "b");
  anchor = clickRows(sel, anchor, ["b"], {}, ORDER);
  assert.deepEqual(ids(sel), []);
});

test("Ctrl adds a row or takes it out, and moves the anchor", () => {
  const sel = new Set(["a"]);
  let anchor = clickRows(sel, "a", ["c"], { ctrlKey: true }, ORDER);
  assert.deepEqual(ids(sel), ["a", "c"]);
  assert.equal(anchor, "c");
  anchor = clickRows(sel, anchor, ["a"], { metaKey: true }, ORDER);
  assert.deepEqual(ids(sel), ["c"]);
  assert.equal(anchor, "a");
});

test("Ctrl on a group card takes all of it, or all of it out", () => {
  const sel = new Set(["g1"]);
  clickRows(sel, "", ["g1", "g2"], { ctrlKey: true }, ORDER);
  assert.deepEqual(ids(sel), ["g1", "g2"]);
  clickRows(sel, "", ["g1", "g2"], { ctrlKey: true }, ORDER);
  assert.deepEqual(ids(sel), []);
});

test("Shift adds the range from the anchor, either way, and keeps the anchor", () => {
  const sel = new Set(["b"]);
  let anchor = clickRows(sel, "b", ["d"], { shiftKey: true }, ORDER);
  assert.deepEqual(ids(sel), ["b", "c", "d", "g1", "g2"]);
  assert.equal(anchor, "b");
  sel.clear();
  anchor = clickRows(sel, "c", ["a"], { shiftKey: true }, ORDER);
  assert.deepEqual(ids(sel), ["a", "b", "c", "g1", "g2"]);
  assert.equal(anchor, "c");
});

test("Shift onto a group card takes the whole card", () => {
  const sel = new Set();
  clickRows(sel, "a", ["g1", "g2"], { shiftKey: true }, ORDER);
  assert.deepEqual(ids(sel), ["a", "b", "g1", "g2"]);
});

test("Shift with no anchor is a plain click; an anchor no longer listed adds only the row", () => {
  const sel = new Set(["a"]);
  assert.equal(clickRows(sel, "", ["c"], { shiftKey: true }, ORDER), "c");
  assert.deepEqual(ids(sel), ["c"]);
  clickRows(sel, "gone", ["d"], { shiftKey: true }, ORDER);
  assert.deepEqual(ids(sel), ["c", "d"]);
});

// A fake clock: run() fires the pending timer.
function fakeTimers() {
  let next = null;
  return {
    setTimeout: (f) => { next = f; return 1; },
    clearTimeout: () => { next = null; },
    run: () => { const f = next; next = null; if (f) f(); },
    get pending() { return !!next; },
  };
}

test("a removal hides the rows at once and asks the engine only when the time runs out", async () => {
  const t = fakeTimers(), removed = [], ended = [];
  const u = undoRemoval(async (x) => { removed.push(x); }, t);
  u.start(["a", "b"], 8000, () => ended.push("ab"));
  assert.deepEqual(ids(u.hiding), ["a", "b"]);
  assert.deepEqual(removed, []);
  t.run();
  assert.deepEqual(removed, [["a", "b"]]);
  assert.deepEqual(ended, ["ab"]);
  await new Promise(setImmediate);
  assert.deepEqual(ids(u.hiding), [], "once the engine answered, its state has them gone");
});

test("Undo brings the rows back and the engine is never asked", () => {
  const t = fakeTimers(), removed = [], ended = [];
  const u = undoRemoval((x) => removed.push(x), t);
  u.start(["a"], 8000, () => ended.push("a"));
  u.undo();
  assert.deepEqual(ids(u.hiding), []);
  assert.equal(t.pending, false);
  assert.deepEqual(ended, ["a"]);
  u.flush();
  assert.deepEqual(removed, []);
});

test("a second removal sends the first one now, and leaving the page sends what waits", () => {
  const t = fakeTimers(), removed = [];
  const u = undoRemoval((x) => { removed.push(x); }, t);
  u.start(["a"], 8000);
  u.start(["b"], 8000);
  assert.deepEqual(removed, [["a"]]);
  assert.ok(u.hiding.has("b"));
  u.flush();
  u.flush();
  assert.deepEqual(removed, [["a"], ["b"]], "each removal is sent once");
});

test("a removal the engine refuses shows the rows again", async () => {
  const t = fakeTimers();
  const u = undoRemoval(() => Promise.reject(new Error("no")), t);
  u.start(["a"], 8000);
  await u.flush().catch(() => {});
  assert.deepEqual(ids(u.hiding), []);
});
