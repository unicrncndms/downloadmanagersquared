"use strict";

const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

globalThis.rpcClient = require("../lib/rpc.js").rpcClient; // the pages load it as a script before host.js
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

// The Downloads window's bridge (window.dmsWindow, set up by the engine): the
// test plays the engine, reading the lines sent and writing lines back.
function fakeWindow() {
  const sent = [];
  const opened = [];
  const timers = [];
  const win = {
    chrome: { webview: {} },
    location: { href: "https://dms.invalid/ui/popup.html?window" },
    setTimeout: (f, ms) => { timers.push({ f, ms }); return timers.length; },
    dmsWindow: {
      send: (line) => sent.push(JSON.parse(line)),
      openUrl: (url) => opened.push(url),
      onLine: null,
    },
  };
  const engine = (m) => win.dmsWindow.onLine(JSON.stringify(m));
  const answer = (method, result) => {
    const req = sent.find((r) => r.method === method && !r.answered);
    req.answered = true;
    engine({ id: req.id, result });
  };
  return { win, sent, opened, timers, engine, answer };
}

const tick = () => new Promise((r) => setImmediate(r));

test("chrome.webview means the Downloads window", () => {
  const { win } = fakeWindow();
  const host = pickHost(win);
  assert.equal(host.userAgent, null);
  assert.equal(host.recipesHere, false);
});

test("the window's calls are correlated with their answers by id", async () => {
  const f = fakeWindow();
  const host = pickHost(f.win);
  const a = host.call("pause", { id: "a" });
  const b = host.call("resume", { id: "b" });
  assert.deepEqual(f.sent.map((r) => [r.method, r.params]), [["pause", { id: "a" }], ["resume", { id: "b" }]]);
  assert.notEqual(f.sent[0].id, f.sent[1].id);
  f.engine({ id: f.sent[1].id, result: "B" }); // out of order
  f.engine({ id: f.sent[0].id, error: "no such download" });
  assert.equal(await b, "B");
  await assert.rejects(a, /no such download/);
});

test("a call the engine doesn't answer fails after 60 s", async () => {
  const f = fakeWindow();
  const host = pickHost(f.win);
  const p = host.call("getState");
  assert.equal(f.timers.at(-1).ms, 60000);
  f.timers.at(-1).f();
  await assert.rejects(p, /did not answer/);
  f.engine({ id: f.sent[0].id, result: {} }); // too late: ignored
});

test("the window's subscribe sends the extension's message shape, connected once the first state and settings are in", async () => {
  const f = fakeWindow();
  const host = pickHost(f.win);
  const got = [];
  host.subscribe((m) => got.push(m));
  assert.equal(got.length, 0); // nothing before the engine answers: no "Engine offline" for a moment
  assert.deepEqual(f.sent.map((r) => r.method).sort(), ["getAutostart", "getSettings", "getState", "version"]);
  f.answer("getState", { downloads: [] });
  f.answer("getSettings", { maxParallel: 3 });
  f.answer("getAutostart", true);
  f.answer("version", "1.4.0");
  await tick();
  assert.deepEqual(got.at(-1), {
    connected: true, error: "", notInstalled: false, engineVersion: "1.4.0",
    state: { downloads: [] }, settings: { maxParallel: 3 }, autostart: true, recipes: null,
  });
  assert.equal(got.length, 1);
  const later = [];
  host.subscribe((m) => later.push(m)); // a later subscriber gets what is known at once
  assert.equal(later.length, 1);
  assert.equal(later[0].connected, true);
  assert.equal(host.version, "1.4.0");
});

test("the window's subscribers get each state and settings broadcast, and nothing else", async () => {
  const f = fakeWindow();
  const host = pickHost(f.win);
  const got = [];
  const stop = host.subscribe((m) => got.push(m));
  for (const m of ["getState", "getSettings", "getAutostart", "version"]) f.answer(m, m === "version" ? "1.0.0" : {});
  await tick();
  const n = got.length;
  f.engine({ event: "state", data: { active: 2 } });
  f.engine({ event: "settings", data: { maxParallel: 5 } });
  f.engine({ event: "resolve", data: {} }); // the extension's, never sent here; ignored
  f.engine("not json");
  assert.equal(got.length, n + 2);
  assert.deepEqual(got.at(-2).state, { active: 2 });
  assert.deepEqual(got.at(-1).settings, { maxParallel: 5 });
  stop();
  f.engine({ event: "state", data: { active: 3 } });
  assert.equal(got.length, n + 2);
});

test("the window keeps autostart after setAutostart, as the background does", async () => {
  const f = fakeWindow();
  const host = pickHost(f.win);
  const got = [];
  host.subscribe((m) => got.push(m));
  for (const m of ["getState", "getSettings", "version"]) f.answer(m, {});
  f.answer("getAutostart", undefined); // false is left out of the answer
  await tick();
  assert.equal(got.at(-1).autostart, false);
  const p = host.call("setAutostart", { on: true });
  f.answer("setAutostart", undefined);
  await p;
  f.engine({ event: "state", data: {} });
  assert.equal(got.at(-1).autostart, true);
});

test("the window switches views by navigating, and opens links in the default browser", () => {
  const f = fakeWindow();
  const host = pickHost(f.win);
  host.openSettings("#pia");
  assert.equal(f.win.location.href, "settings.html#pia");
  host.openDownloads();
  assert.equal(f.win.location.href, "popup.html?window");
  host.openUrl("https://example.com/releases");
  assert.deepEqual(f.opened, ["https://example.com/releases"]);
  assert.equal(host.forgetRecipe, undefined);
});

test("a subscriber that throws is reported to the engine's log, not lost in the closed DevTools", async () => {
  const f = fakeWindow();
  const logged = [];
  f.win.dmsWindow.log = (m) => logged.push(m);
  const host = pickHost(f.win);
  host.subscribe(() => { throw new Error("accentCSS is not defined"); });
  f.answer("getState", { downloads: [] });
  f.answer("getSettings", {});
  f.answer("getAutostart", false);
  f.answer("version", "1");
  await tick();
  assert.match(logged[0], /accentCSS is not defined/);
});

// The window carries the extension's lib folder whole (build.ps1), and the
// pages load ../lib/X: every script they name must exist there.
test("every script a page loads exists", () => {
  const ui = path.join(__dirname, "../ui");
  for (const page of fs.readdirSync(ui).filter((n) => n.endsWith(".html"))) {
    for (const [, src] of fs.readFileSync(path.join(ui, page), "utf8").matchAll(/<script src="([^"]+)"/g)) {
      assert.ok(fs.existsSync(path.join(ui, src)), `${page} loads ${src}`);
    }
  }
});
