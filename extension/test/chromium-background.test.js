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
