"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { listRows } = require("../lib/rows.js");

const SECTION = { running: "active", verifying: "active", metadata: "active", checking: "active", seeding: "seeding", queued: "queued",
  paused: "paused", completed: "done", failed: "needs", choosing: "needs" };
const dl = (id, status, extra) => ({ kind: "http", id, status, section: SECTION[status], filename: id, url: `https://example.com/${id}`, size: 0, done: 0, links: {}, actions: {}, ...extra });
const summary = (extra) => ({ id: "g", name: "discs", parts: false, count: 2, items: 2, done: 0, size: 200e6, got: 0, speed: 0, finished: false,
  label: "Downloading", tone: "", pause: false, resume: false, partialFile: false, order: [], ...extra });
const ui = (extra) => ({ query: "", folded: new Set(), selected: new Set(), opened: "", ...extra });
const ids = (r) => r.sections.map((s) => [s.name, s.rows.map((x) => (x.d ? x.d.id : `${x.summary.name}:${x.items.map((i) => i.d.id).join(",")}`))]);

// The engine lists oldest first.
const mixed = [
  dl("done1", "completed"), dl("run1", "running"), dl("q", "queued"), dl("seed", "seeding", { kind: "torrent" }),
  dl("p", "paused"), dl("fail", "failed"), dl("run2", "running"), dl("done2", "completed"),
  dl("choose", "choosing", { kind: "torrent" }), dl("perr", "paused", { error: "Paused: the folder was removed", section: "needs" }),
  dl("ask", "running", { section: "needs", folder: { on: true } }), dl("check", "checking", { kind: "torrent" }),
];

test("sections in order with counts; newest first in each", () => {
  const r = listRows({ downloads: mixed }, ui());
  assert.deepEqual(ids(r), [
    ["Needs you", ["ask", "perr", "choose", "fail"]], ["Downloading", ["check", "run2", "run1"]], ["Seeding", ["seed"]],
    ["Queue", ["q"]], ["Paused", ["p"]], ["Finished", ["done2", "done1"]],
  ]);
  assert.deepEqual(r.sections.map((s) => s.count), [4, 3, 1, 1, 1, 2]);
  assert.equal(r.matched, 12);
});

test("queued rows list in the order they will start and are numbered from 1; no number elsewhere", () => {
  const r = listRows({ downloads: [dl("run", "running"), dl("q1", "queued"), dl("q2", "queued"), dl("q3", "queued")] }, ui());
  assert.deepEqual(r.sections[1].rows.map((x) => [x.d.id, x.place]), [["q1", 1], ["q2", 2], ["q3", 3]]);
  assert.equal(r.sections[0].rows[0].place, 0);
});

test("a folded section keeps its rows and count but leaves the Shift-click order", () => {
  const r = listRows({ downloads: [dl("a", "running"), dl("b", "queued"), dl("c", "paused")] }, ui({ folded: new Set(["Queue"]) }));
  assert.deepEqual(r.sections.map((s) => [s.name, s.folded, s.count, s.rows.length]), [["Downloading", false, 1, 1], ["Queue", true, 1, 1], ["Paused", false, 1, 1]]);
  assert.deepEqual(r.shown, [["a"], ["c"]]);
});

test("the search narrows the list to what it finds", () => {
  const r = listRows({ downloads: [dl("alpha", "running"), dl("beta", "running"), dl("alps", "paused")] }, ui({ query: "AL" }));
  assert.deepEqual(ids(r), [["Downloading", ["alpha"]], ["Paused", ["alps"]]]);
  assert.equal(r.matched, 2);
  const none = listRows({ downloads: [dl("a", "running")] }, ui({ query: "zzz" }));
  assert.deepEqual([none.sections, none.matched, none.shown], [[], 0, []]);
});

test("a Metalink's files are one group where its first file sorts, in the engine's order; no summary, plain rows", () => {
  const list = [dl("a", "running", { group: "g" }), dl("x", "running"), dl("b", "queued", { group: "g" }), dl("c", "queued", { group: "h" })];
  const groups = [summary({ order: ["b", "a"] }), summary({ id: "h", count: 1, items: 1, order: ["c"] })];
  const r = listRows({ downloads: list, groups }, ui());
  assert.deepEqual(ids(r), [["Downloading", ["x"]], ["Queue", ["discs:b,a", "c"]]]); // in the section of its first file; h is down to one file
  assert.equal(r.sections[1].rows[0].summary, groups[0]);
  assert.deepEqual(r.shown, [["x"], ["b", "a"], ["c"]]);
  assert.equal(r.sections[1].rows[0].place, 1);
  assert.deepEqual(ids(listRows({ downloads: list }, ui())), [["Downloading", ["x", "a"]], ["Queue", ["b", "c"]]]);
});

test("row forms: card, a finished download as a line, a group's fetched part as a part line", () => {
  const list = [dl("run", "running"), dl("fin", "completed"),
    dl("p1", "completed", { group: "parts:x", section: "active" }), dl("p2", "running", { group: "parts:x" })];
  const r = listRows({ downloads: list, groups: [summary({ id: "parts:x", parts: true, order: ["p1", "p2"] })] }, ui());
  const [active, done] = r.sections;
  assert.equal(active.rows.find((x) => x.d && x.d.id === "run").form, "card");
  assert.deepEqual(active.rows.find((x) => x.group).items.map((i) => [i.d.id, i.form]), [["p1", "part"], ["p2", "card"]]);
  assert.equal(done.rows[0].form, "line");
});

test("its Details being open makes a line or a part a card", () => {
  const list = [dl("fin", "completed"), dl("p1", "completed", { group: "parts:x", section: "active" }), dl("p2", "running", { group: "parts:x" })];
  const state = { downloads: list, groups: [summary({ id: "parts:x", parts: true, order: ["p1", "p2"] })] };
  assert.equal(listRows(state, ui({ opened: "fin" })).sections[1].rows[0].form, "card");
  assert.equal(listRows(state, ui({ opened: "p1" })).sections[0].rows[0].items[0].form, "card");
  assert.deepEqual(listRows(state, ui({ opened: "p2" })).sections[0].rows[0].items.map((i) => i.form), ["part", "card"]);
});

test("Shift-click order is every row's ids, a group's together, skipping folded sections", () => {
  const list = [dl("a", "running", { group: "g" }), dl("b", "running", { group: "g" }), dl("s", "seeding"), dl("f", "completed")];
  const r = listRows({ downloads: list, groups: [summary({ order: ["a", "b"] })] }, ui({ folded: new Set(["Seeding"]) }));
  assert.deepEqual(r.shown, [["a", "b"], ["f"]]);
});

test("the selection loses downloads that are gone, and says when the opened one is", () => {
  const state = { downloads: [dl("a", "running"), dl("b", "paused")] };
  const r = listRows(state, ui({ selected: new Set(["a", "gone"]), opened: "gone" }));
  assert.deepEqual([...r.selected], ["a"]);
  assert.equal(r.openedGone, true);
  assert.equal(listRows(state, ui({ opened: "b" })).openedGone, false);
  assert.equal(listRows(state, ui()).openedGone, false);
  // A search hiding a selected row doesn't unselect it.
  assert.deepEqual([...listRows(state, ui({ selected: new Set(["b"]), query: "a" })).selected], ["b"]);
});
