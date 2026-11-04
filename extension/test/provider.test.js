"use strict";

const test = require("node:test");
const assert = require("node:assert");
const { handOverProvider } = require("../lib/provider.js");

// browser.storage.local, with what the extension stored before upgrading.
function storage(initial) {
  const store = { ...initial };
  return {
    store,
    get: async (k) => (k in store ? { [k]: store[k] } : {}),
    remove: async (k) => { delete store[k]; },
  };
}

function engine(returns) {
  const calls = [];
  return { calls, call: async (method, params) => { calls.push([method, params]); return returns(params); } };
}

test("the stored choice is sent once to an engine without one, then removed", async () => {
  const s = storage({ vpnProvider: "pia" });
  const e = engine((p) => ({ vpnProvider: p.vpnProvider }));
  await handOverProvider(s, { vpnProvider: "" }, e.call);
  assert.deepEqual(e.calls, [["setSettings", { vpnProvider: "pia" }]]);
  assert.deepEqual(s.store, {});
});

test("the engine's own choice wins, and the stored one is removed", async () => {
  const s = storage({ vpnProvider: "pia" });
  const e = engine(() => ({}));
  await handOverProvider(s, { vpnProvider: "conf" }, e.call);
  assert.deepEqual(e.calls, []);
  assert.deepEqual(s.store, {});
});

test("nothing stored: nothing sent", async () => {
  const s = storage({ other: 1 });
  const e = engine(() => ({}));
  await handOverProvider(s, { vpnProvider: "" }, e.call);
  assert.deepEqual(e.calls, []);
  assert.deepEqual(s.store, { other: 1 });
});

test("an engine that doesn't keep the choice yet leaves it stored", async () => {
  const s = storage({ vpnProvider: "pia" });
  const e = engine(() => ({ saveDir: "C:\\" }));
  await handOverProvider(s, {}, e.call);
  assert.deepEqual(s.store, { vpnProvider: "pia" });
});
