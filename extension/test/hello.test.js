"use strict";

// background/engine.js: the hello says whether this browser can run a resolve, by feature, never by name.

const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { rpcClient } = require("../lib/rpc.js");
const { chunkJoiner } = require("../lib/chunks.js");

function helloOf(extra) {
  const sent = [];
  const ev = () => ({ addListener() {} });
  const port = { postMessage: (m) => sent.push(m), onMessage: ev(), onDisconnect: ev(), disconnect() {} };
  const browser = { runtime: { connectNative: () => port, getManifest: () => ({ version: "1" }) }, ...extra };
  const ctx = vm.createContext({ browser, rpcClient, chunkJoiner, navigator: { userAgent: "UA" }, setTimeout() {}, clearTimeout() {}, Promise });
  vm.runInContext(fs.readFileSync(path.join(__dirname, "../background/engine.js"), "utf8") + "\nEngine.connect();", ctx);
  return sent.find((m) => m.method === "hello").params;
}

test("hello says resolves when proxy.onRequest and containers exist", () => {
  assert.equal(helloOf({ proxy: { onRequest: {} }, contextualIdentities: {} }).resolves, true);
});

test("hello says no resolves without either", () => {
  assert.equal(helloOf({ contextualIdentities: {} }).resolves, false);
  assert.equal(helloOf({ proxy: { onRequest: {} } }).resolves, false);
  assert.equal(helloOf({}).resolves, false);
});
