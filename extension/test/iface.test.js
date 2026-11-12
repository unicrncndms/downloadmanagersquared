"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { ifaceHue } = require("../lib/iface.js");

const state = { interfaces: [{ id: "wifi", color: 3 }, { id: "eth", color: 0 }, { id: "eth~vpn", tunnel: true, of: "eth", color: 0 }] };

test("a connection is shown in the colour the engine kept for it", () => {
  assert.equal(ifaceHue("wifi", state), "var(--c3)");
  assert.equal(ifaceHue("eth", state), "var(--c0)");
});

test("a tunnel takes its interface's colour", () => {
  assert.equal(ifaceHue("wifi~vpn", state), "var(--c3)");
});

test("without a colour it is the place in the list, as before", () => {
  const old = { interfaces: [{ id: "a" }, { id: "b" }] };
  assert.equal(ifaceHue("b", old), "var(--c1)");
  assert.equal(ifaceHue("gone", old), "var(--c0)");
  assert.equal(ifaceHue("a", undefined), "var(--c0)");
});
