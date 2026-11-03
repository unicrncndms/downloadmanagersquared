"use strict";

// Loads every background script, in manifest order, and the resolver's content
// script against a browser that does nothing: a script that throws while
// loading fails here instead of leaving the extension dead.

const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const root = path.join(__dirname, "..");
const manifest = JSON.parse(fs.readFileSync(path.join(root, "manifest.json"), "utf8"));

// Anything: every property is another anything, every call returns a promise
// of an empty result. Never thenable itself, so awaiting it doesn't hang.
function anything() {
  return new Proxy(function () {}, {
    get(_, prop) {
      if (prop === "then") return undefined;
      if (prop === Symbol.iterator) return function* () {};
      if (prop === Symbol.toPrimitive) return () => "";
      return anything();
    },
    apply: () => promised(),
    construct: () => anything(),
  });
}

// What a browser.* call resolves to: an empty array that also answers any
// property (a port's onMessage, a window's tabs) with anything.
function promised() {
  const empty = new Proxy([], { get: (t, p) => (p in t ? t[p] : p === "then" ? undefined : anything()) });
  const p = Promise.resolve(empty);
  return new Proxy(p, {
    get: (t, prop) => (prop in t ? t[prop].bind(t) : anything()),
  });
}

function context(extra) {
  const ctx = {
    browser: anything(), console: { log() {}, warn() {}, error() {}, info() {} },
    // Timers never fire: loading is what's tested, and a pending one would keep the test running.
    setTimeout: () => 0, clearTimeout() {}, setInterval: () => 0, clearInterval() {}, URL, URLSearchParams, Promise,
    navigator: { userAgent: "test" }, screen: { availWidth: 1920, availHeight: 1080, availLeft: 0, availTop: 0 },
    ...extra,
  };
  ctx.window = ctx;
  return vm.createContext(ctx);
}

function load(ctx, file) {
  vm.runInContext(fs.readFileSync(path.join(root, file), "utf8"), ctx, { filename: file });
}

// Async startup work (e.g. Resolver.init) may reject against the fake browser;
// only throwing while loading counts.
process.on("unhandledRejection", () => {});

test("every background script loads in manifest order", () => {
  const ctx = context();
  for (const file of manifest.background.scripts) {
    assert.doesNotThrow(() => load(ctx, file), file);
  }
  for (const name of ["Engine", "Resolver", "Capture"]) {
    assert.equal(vm.runInContext(`typeof ${name}`, ctx), "object", `${name} is defined`);
  }
});

test("the resolver's content script loads in a page", () => {
  const ctx = context({ document: anything(), getComputedStyle: anything(), location: anything() });
  assert.doesNotThrow(() => load(ctx, "resolver-content.js"));
});

test("a script that throws while loading fails the smoke test", () => {
  const ctx = context();
  assert.throws(() => vm.runInContext("\"use strict\"; undefinedThing.call();", ctx));
});
