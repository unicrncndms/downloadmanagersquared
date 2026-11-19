"use strict";

// The background scripts against a Chromium-shaped fake browser (fake-background.js, chromium: true):
// no proxy, containers, theme or menus.onShown, no blocking webRequest, strict cookie and download calls.

const test = require("node:test");
const assert = require("node:assert");
const { load } = require("./fake-background.js");

const named = (b, name) => b.calls.filter((c) => c[0] === name);

test("Chromium startup throws nothing and registers no blocking, proxy or theme listener", async () => {
  const b = load({ chromium: true });
  await b.settle();
  for (const name of ["webRequest.onHeadersReceived", "webRequest.onBeforeRequest", "proxy.onRequest", "theme.onUpdated"]) assert.ok(!b.listeners[name], name);
  for (const name of ["downloads.onCreated", "webRequest.onSendHeaders", "contextMenus.onClicked", "runtime.onInstalled", "runtime.onConnect"]) assert.ok(b.listeners[name], name);
  assert.equal(b.Resolver.loaded, true);
  assert.equal(await b.Resolver.ready, undefined);
});

test("Chromium: menus are made on install only, with no onShown hook; a start makes none", async () => {
  const b = load({ chromium: true });
  await b.settle();
  assert.equal(named(b, "contextMenus.create").length, 0);
  await Promise.all(b.fire("runtime.onInstalled", { reason: "update" }));
  const made = named(b, "contextMenus.create");
  assert.deepEqual(made.map((c) => c[1].id), ["dms-link", "dms-page"]);
  assert.equal(made[1][1].visible, true);
});

test("Chromium: the referer hook asks for extraHeaders; Firefox's does not", () => {
  assert.ok(load({ chromium: true }).specs["webRequest.onSendHeaders"].includes("extraHeaders"));
  assert.deepEqual(load().specs["webRequest.onSendHeaders"], ["requestHeaders"]);
});

test("Chromium: a large download is handed off, and handed back, with no cookieStoreId or firstPartyDomain", async () => {
  const b = load({ chromium: true });
  await b.settle();
  b.connect();
  await Promise.all(b.fire("downloads.onCreated", { id: 1, url: "https://cdn.test/big.iso", totalBytes: 200e6, filename: "C:\\D\\big.iso" }));
  await b.settle();
  assert.equal(b.engine.find((c) => c.method === "add").params.url, "https://cdn.test/big.iso");
  b.Engine.call = () => Promise.reject(new Error("no")); // the engine refuses: the browser gets it back
  await Promise.all(b.fire("downloads.onCreated", { id: 2, url: "https://cdn.test/b2.iso", totalBytes: 200e6, filename: "b2.iso" }));
  await b.settle();
  assert.ok(named(b, "downloads.download").length > 0);
  assert.ok(named(b, "cookies.getAll").length > 0);
  assert.ok(!/cookieStoreId|firstPartyDomain|partitionKey/.test(JSON.stringify(b.calls)));
});

test("Chromium: the context menu hands off a tab that has no cookieStoreId", async () => {
  const b = load({ chromium: true });
  await b.settle();
  b.connect();
  await Promise.all(b.fire("contextMenus.onClicked", { menuItemId: "dms-link", linkUrl: "https://cdn.test/f.zip" }, { id: 3, url: "https://site.test/" }));
  await b.settle();
  assert.equal(b.engine.find((c) => c.method === "add").params.url, "https://cdn.test/f.zip");
});

test("Chromium: the theme is never read and no accent is sent; the badge goes through action", async () => {
  const b = load({ chromium: true });
  await b.settle();
  b.connect();
  b.Engine.emit("badge", { running: 1, needs: [] });
  await b.settle();
  assert.equal(named(b, "theme.getCurrent").length, 0);
  assert.ok(!b.engine.some((c) => c.method === "setSettings"));
  assert.ok(named(b, "browserAction.setBadgeText").length > 0); // the fake names action's calls after browserAction's
});

test("Firefox: menus are still made at every start, and the blocking hook is registered", async () => {
  const b = load();
  await b.settle();
  assert.deepEqual(named(b, "contextMenus.create").map((c) => c[1].id), ["dms-link", "dms-page"]);
  assert.ok(b.listeners["webRequest.onHeadersReceived"]);
});

// The engine resolves Chrome's downloads in a browser of its own: Chrome hands it the landing page's
// login (a host-only cookie by its address), the download's page history and the accent.
test("Chromium: pageInfo answers with the page's cookies, pages and accent; a private window's login stays", async () => {
  const jar = { 0: [
    { name: "sid", value: "1", domain: "site.test", hostOnly: true, path: "/", secure: true, httpOnly: true, expirationDate: 2000000000 },
    { name: "pref", value: "x", domain: ".site.test", hostOnly: false, path: "/" },
  ] };
  const b = load({ chromium: true, cookieJar: jar });
  await b.settle();
  b.Capture.pagesById.d1 = ["https://site.test/", "https://site.test/get"];
  b.Engine.emit("pageInfo", { id: "d1", pageUrl: "https://site.test/get", cookieStore: "0" });
  await b.settle();
  const said = b.engine.filter((c) => c.method === "pageInfo");
  assert.equal(said.length, 1);
  const p = said[0].params;
  assert.equal(p.id, "d1");
  assert.deepEqual(p.cookies, [
    { name: "sid", value: "1", path: "/", secure: true, httpOnly: true, expires: 2000000000, url: "https://site.test/" },
    { name: "pref", value: "x", path: "/", secure: false, httpOnly: false, domain: ".site.test" },
  ]);
  assert.deepEqual(p.pages, ["https://site.test/", "https://site.test/get"]);
  assert.ok(p.tokens && Object.keys(p.tokens).length > 0, "no accent tokens");

  b.Engine.emit("pageInfo", { id: "d1", pageUrl: "https://site.test/get", cookieStore: "1" });
  await b.settle();
  assert.deepEqual(b.engine.filter((c) => c.method === "pageInfo")[1].params.cookies, []);
});

test("Chromium: a stepBack from the engine is learned as a recording in this browser would be", async () => {
  const b = load({ chromium: true });
  await b.settle();
  b.Capture.pagesById.d1 = ["https://site.test/", "https://site.test/a", "https://site.test/get"];
  b.Engine.emit("stepBack", { id: "d1", url: "https://site.test/" });
  await b.settle();
  const want = { ...b.Capture.stepBacks };
  const c = load({ chromium: true });
  await c.settle();
  c.Capture.pagesById.d1 = b.Capture.pagesById.d1;
  c.Capture.learnStepBack("d1", "https://site.test/");
  assert.deepEqual(want, c.Capture.stepBacks);
  assert.ok(Object.keys(want).length === 1, "nothing learned");
});

test("A step-back learned in one browser is used by another through the engine (ticket 36)", async () => {
  const engineStepBacks = {};
  const own = () => ({ local: {}, session: {}, windows: new Map(), nextId: { v: 100 }, engineStepBacks });
  const ff = load({ shared: own() });
  ff.connect();
  await ff.settle();
  ff.Capture.pagesById.d1 = ["https://site.test/list", "https://site.test/item", "https://site.test/get"];
  ff.Capture.learnStepBack("d1", "https://site.test/get");
  await ff.settle();
  const chrome = load({ shared: own(), chromium: true });
  await chrome.settle();
  chrome.connect();
  await chrome.settle();
  assert.equal(chrome.Capture.landingPage(["https://site.test/list", "https://site.test/item", "https://site.test/get"]), "https://site.test/get");
  // A new recording replaces what the engine knew; the event carries it to the others.
  ff.Capture.learnStepBack("d1", "https://site.test/item");
  await ff.settle();
  assert.equal(engineStepBacks["site.test"], 1);
  chrome.Engine.emit("stepBacks", { ...engineStepBacks });
  await chrome.settle();
  assert.equal(chrome.Capture.stepBacks["site.test"], 1);
});

test("Chromium: an open settings page gets the engine's recipes as soon as they are synced (ticket 37)", async () => {
  const c = load({ chromium: true });
  c.shared.engineRecipes = { "a.test": { steps: [], learned: 1 } };
  const c2 = load({ chromium: true, shared: c.shared });
  await c2.settle();
  const sent = [];
  const ev = () => ({ addListener() {} });
  c2.fire("runtime.onConnect", { name: "ui", sender: { url: "chrome-extension://x/ui/settings.html" }, postMessage: (m) => sent.push(m), onMessage: ev(), onDisconnect: ev() });
  c2.connect();
  await c2.settle();
  assert.ok(sent.length && sent[sent.length - 1].recipes["a.test"], "the page never heard of the recipe");
});

test("Chromium: a tab's page history outlives the background going to sleep", async () => {
  const c = load({ chromium: true });
  await c.settle();
  for (const url of ["https://www.google.com/", "https://site.test/list", "https://site.test/item"]) c.fire("webNavigation.onCommitted", { frameId: 0, tabId: 7, url });
  await c.settle();
  const woken = load({ chromium: true, shared: c.shared }); // the worker woke with nothing in memory
  await woken.settle();
  woken.fire("webNavigation.onCommitted", { frameId: 0, tabId: 7, url: "https://site.test/get" });
  await woken.settle();
  assert.deepEqual(woken.Capture.history.get(7), ["https://www.google.com/", "https://site.test/list", "https://site.test/item", "https://site.test/get"]);
});
