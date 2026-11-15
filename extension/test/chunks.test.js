"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { chunkJoiner } = require("../lib/chunks.js");

const cut = (obj, id, n) => {
  const s = JSON.stringify(obj);
  const size = Math.ceil(s.length / n);
  return Array.from({ length: n }, (_, i) => ({ chunk: i, of: n, id, data: s.slice(i * size, (i + 1) * size) }));
};

test("chunks in any order join to the message", () => {
  const add = chunkJoiner();
  const msg = { event: "state", data: { downloads: [1, 2, 3], name: "é\"x" } };
  const [a, b, c] = cut(msg, 1, 3);
  assert.equal(add(c), null);
  assert.equal(add(a), null);
  assert.deepEqual(add(b), msg);
});

test("a line with a missing chunk is forgotten once newer ones push it out", () => {
  const add = chunkJoiner(2);
  const [first, second] = cut({ a: 1 }, 1, 2); // the second comes too late
  add(first);
  for (const id of [2, 3]) add(cut({ b: id }, id, 2)[0]);
  assert.equal(add(second), null); // id 1 was dropped: this one starts over and waits
});

test("repeated, out of range or malformed chunks are ignored", () => {
  const add = chunkJoiner();
  const [a, b] = cut({ ok: true }, 5, 2);
  assert.equal(add(a), null);
  assert.equal(add(a), null);
  assert.equal(add({ chunk: 9, of: 2, id: 5, data: "x" }), null);
  assert.equal(add({ id: 6 }), null);
  assert.deepEqual(add(b), { ok: true });
});
