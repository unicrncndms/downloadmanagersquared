"use strict";

const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const { pickHost } = require("../ui/host.js");

// A browser that records what the pages send and lets the test play the background.
function fakeBrowser() {
  const sent = [];
  const ports = [];
  const tabs = [];
  const browser = {
    runtime: {
      sendMessage: (m) => { sent.push(m); return Promise.resolve({ echo: m }); },
      connect: (info) => {
        const listeners = [];
        const port = {
          info, disconnected: false,
          onMessage: { addListener: (f) => listeners.push(f) },
          disconnect() { this.disconnected = true; },
          deliver: (m) => listeners.forEach((f) => f(m)),
        };
        ports.push(port);
        return port;
      },
      getManifest: () => ({ version: "1.2.3" }),
    },
    tabs: { create: (o) => { tabs.push(o); return Promise.resolve(); } },
  };
  return { browser, sent, ports, tabs };
}

test("a Host already set, as the harness does, is kept", () => {
  const own = { call() {} };
  const { browser } = fakeBrowser();
  assert.equal(pickHost({ Host: own, browser, chrome: { webview: {} } }), own);
});

test("browser.runtime means the extension", () => {
  const { browser } = fakeBrowser();
  const host = pickHost({ browser, navigator: { userAgent: "Firefox/152" } });
  assert.equal(host.version, "1.2.3");
  assert.equal(host.userAgent, "Firefox/152");
  assert.equal(host.recipesHere, true);
});

test("with neither, there is no Host", () => {
  assert.equal(pickHost({}), null);
});

test("the extension's call goes to the background", async () => {
  const f = fakeBrowser();
  const host = pickHost({ browser: f.browser, navigator: {} });
  const r = await host.call("pause", { id: "a" });
  assert.deepEqual(f.sent, [{ type: "call", method: "pause", params: { id: "a" } }]);
  assert.deepEqual(r, { echo: f.sent[0] });
});

test("the extension's subscribe listens on the ui port", () => {
  const f = fakeBrowser();
  const host = pickHost({ browser: f.browser, navigator: {} });
  const got = [];
  const stop = host.subscribe((m) => got.push(m));
  assert.equal(f.ports.length, 1);
  assert.deepEqual(f.ports[0].info, { name: "ui" });
  f.ports[0].deliver({ connected: true });
  assert.deepEqual(got, [{ connected: true }]);
  stop();
  assert.equal(f.ports[0].disconnected, true);
});

test("the extension opens settings, links and forgets recipes through the background and tabs", async () => {
  const f = fakeBrowser();
  const host = pickHost({ browser: f.browser, navigator: {} });
  await host.openSettings("#pia");
  await host.forgetRecipe("example.com");
  await host.openUrl("https://example.com/releases");
  assert.deepEqual(f.sent, [{ type: "openSettings", hash: "#pia" }, { type: "forgetRecipe", host: "example.com" }]);
  assert.deepEqual(f.tabs, [{ url: "https://example.com/releases" }]);
});

test("loaded as a page script, host.js sets window.Host", () => {
  const f = fakeBrowser();
  const ctx = { browser: f.browser, navigator: { userAgent: "x" } };
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync(path.join(__dirname, "../ui/host.js"), "utf8"), ctx);
  assert.equal(ctx.Host.version, "1.2.3");
});
