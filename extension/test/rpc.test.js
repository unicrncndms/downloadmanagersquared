"use strict";

// lib/rpc.js, the request/answer pairing both clients use, on its own (host.test.js only reaches it through the background).

const test = require("node:test");
const assert = require("node:assert/strict");
const { rpcClient } = require("../lib/rpc.js");

function client() {
  const sent = [], timers = [];
  const c = rpcClient((m) => sent.push(m), (fn, ms) => timers.push({ fn, ms }));
  return { c, sent, timers };
}

test("an answer settles its own call, by id, whatever the order", async () => {
  const { c, sent } = client();
  const a = c.call("one"), b = c.call("two", { x: 1 });
  assert.deepEqual(sent, [{ id: 1, method: "one", params: {} }, { id: 2, method: "two", params: { x: 1 } }]);
  c.answer({ id: 2, result: "B" });
  c.answer({ id: 1, error: "nope" });
  c.answer({ id: 9, result: "stray" }); // nobody waits for it
  assert.equal(await b, "B");
  await assert.rejects(a, /nope/);
  assert.equal(c.pending.size, 0);
});

test("a call fails after a minute without an answer, and drop fails the rest", async () => {
  const { c, timers } = client();
  const slow = c.call("slow"), gone = c.call("gone");
  assert.equal(timers[0].ms, 60000);
  timers[0].fn();
  await assert.rejects(slow, /did not answer/);
  c.drop("connection lost");
  await assert.rejects(gone, /connection lost/);
  timers[1].fn(); // its timer fires late: nothing left to fail
  assert.equal(c.pending.size, 0);
});

test("a call that waits for the user's folder dialog has no time limit", async () => {
  const { c, timers } = client();
  const picked = c.call("browseFolder", { start: "" });
  assert.equal(timers.length, 0); // two minutes in the dialog is no "engine did not answer"
  c.answer({ id: 1, result: { dir: "D:/x" } });
  assert.deepEqual(await picked, { dir: "D:/x" });
});
