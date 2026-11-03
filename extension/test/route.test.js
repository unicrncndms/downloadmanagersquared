"use strict";

const test = require("node:test");
const assert = require("node:assert");
const { containerPort } = require("../lib/route.js");

const ifaces = [{ id: "eth", present: true, socksPort: 50001 }, { id: "wifi", present: false, socksPort: 50002 }];

test("a container goes through its interface's proxy", () => {
  assert.equal(containerPort("eth", true, ifaces), 50001);
});

test("no engine, an absent or unknown interface: nowhere", () => {
  assert.equal(containerPort("eth", false, ifaces), 0);
  assert.equal(containerPort("wifi", true, ifaces), 0);
  assert.equal(containerPort("lte", true, ifaces), 0);
  assert.equal(containerPort("eth", true, null), 0);
});
