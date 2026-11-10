"use strict";

// The background scripts against a fake browser (fake-background.js): who
// owns a request, capture through downloads.onCreated alone, resolver jobs
// that carry on across a restart of the background, and learn jobs.

const test = require("node:test");
const assert = require("node:assert");
const { load } = require("./fake-background.js");

const PAGE = "https://site.test/get/1";

// A job for download d1 on eth, recording (no recipe yet): its window and tab.
async function recording(b, extra = {}) {
  b.Engine.emit("resolve", { id: "d1", iface: "eth", ifaceName: "Ethernet", pageUrl: PAGE, ...extra });
  await b.settle();
  const job = b.Resolver.jobs.get("d1|eth");
  return job;
}

const engineCalls = (b, method) => b.engine.filter((c) => c.method === method).map((c) => c.params);
const message = (b, job, msg) => b.fire("runtime.onMessage", { resolver: true, ...msg }, { tab: { id: job.tabId, url: PAGE } });

test("every browser listener is registered before anything is awaited", () => {
  const b = load();
  for (const name of [
    "proxy.onRequest", "webRequest.onBeforeRequest", "webRequest.onHeadersReceived", "webRequest.onSendHeaders",
    "webRequest.onBeforeSendHeaders", "downloads.onCreated", "webNavigation.onCommitted", "tabs.onUpdated",
    "tabs.onCreated", "tabs.onRemoved", "windows.onRemoved", "contextualIdentities.onRemoved",
    "runtime.onMessage", "runtime.onConnect", "contextMenus.onClicked",
  ]) assert.ok(b.listeners[name], name);
});

test("a resolver tab and the tab it opens go through its interface; an unrelated tab doesn't", async () => {
  const b = load();
  await b.settle();
  b.connect();
  const job = await recording(b);
  assert.equal(job.mode, "record");
  b.fire("tabs.onCreated", { id: 900, openerTabId: job.tabId, windowId: 1 });
  const socks = { type: "socks", host: "127.0.0.1", port: 5001, proxyDNS: true };
  // Keyed by tab alone, as on Chromium: no cookie store.
  assert.deepEqual(b.Resolver.proxyFor({ tabId: job.tabId }), socks);
  assert.deepEqual(b.Resolver.proxyFor({ tabId: 900 }), socks);
  assert.deepEqual(b.Resolver.proxyFor({ tabId: 5, cookieStoreId: "firefox-default" }), { type: "direct" });
  // A tab opened in the job's window without an opener is the job's too.
  b.fire("tabs.onCreated", { id: 901, windowId: job.windowId });
  assert.equal(b.Resolver.ownerOf({ tabId: 901 }).job, job);
  // In Firefox the container is an extra key: a request with no tab still counts.
  const store = b.Resolver.containers.eth;
  assert.equal(b.Resolver.ownerOf({ tabId: -1, cookieStoreId: store }).job, job);
  assert.equal(b.Resolver.ownerOf({ tabId: 5, cookieStoreId: "firefox-default" }), null);
  // No port for the interface: the job's requests are cancelled, the user's aren't.
  b.Engine.state = { interfaces: [] };
  assert.deepEqual(b.Resolver.cancelUnrouted({ tabId: 900 }), { cancel: true });
  assert.deepEqual(b.Resolver.cancelUnrouted({ tabId: 5 }), {});
});

test("with no headers hook, every capture case hands off through downloads.onCreated", async () => {
  const b = load();
  await b.settle();
  b.connect();
  for (const url of ["https://site.test/", "https://site.test/file-page"]) {
    b.fire("webNavigation.onCommitted", { frameId: 0, tabId: 7, url });
  }
  const created = async (item) => {
    await Promise.all(b.fire("downloads.onCreated", { id: 1, url: item.url || "https://cdn.test/x", ...item }));
    await b.settle();
  };

  await created({ url: "https://site.test/dl?k=1", finalUrl: "https://cdn.test/big.iso", totalBytes: 200e6, filename: "C:\\D\\big.iso", referrer: "https://site.test/file-page" });
  let add = engineCalls(b, "add").pop();
  assert.equal(add.url, "https://cdn.test/big.iso"); // finalUrl, from the item
  assert.equal(add.filename, "big.iso");
  assert.equal(add.referer, "https://site.test/file-page");
  assert.equal(add.pageUrl, "https://site.test/"); // the tab's history, one page back
  assert.equal(add.torrent, false);
  assert.ok(b.calls.some((c) => c[0] === "downloads.cancel"));

  await created({ url: "https://site.test/a.torrent", totalBytes: 900 });
  assert.equal(engineCalls(b, "add").pop().torrent, true);

  const before = b.engine.length;
  await created({ totalBytes: 5e6 });                                  // small
  await created({ totalBytes: 200e6, mime: "text/html" });             // a page
  await created({ totalBytes: 200e6, byExtensionId: "dms@test" });     // our own
  b.Capture.bypass.add("https://cdn.test/given-back");
  await created({ url: "https://cdn.test/given-back", totalBytes: 200e6 }); // handed back to the browser
  assert.equal(b.engine.length, before);
});

test("a download in a resolver job's container is its link, not a new download", async () => {
  const b = load();
  await b.settle();
  b.connect();
  const job = await recording(b);
  await Promise.all(b.fire("downloads.onCreated", { id: 2, url: "https://cdn.test/fresh", totalBytes: 5, cookieStoreId: b.Resolver.containers.eth }));
  await b.settle();
  assert.deepEqual(engineCalls(b, "add"), []);
  const r = engineCalls(b, "resolved")[0];
  assert.equal(r.id, "d1");
  assert.equal(r.url, "https://cdn.test/fresh");
  assert.ok(job.done);
});

test("a job reloaded from storage after a restart carries on", async () => {
  const b = load();
  await b.settle();
  b.connect();
  const job = await recording(b);
  message(b, job, { type: "recorded", step: { tag: "a", text: "Download" } });
  await b.settle();

  // The background restarts: same storage and windows, nothing in memory.
  const b2 = load({ shared: b.shared, now: b.clock.now + 1000 });
  await b2.settle();
  b2.connect();
  const back = b2.Resolver.jobs.get("d1|eth");
  assert.ok(back, "the job is back");
  assert.equal(back.mode, "record");
  assert.equal(back.recorded.length, 1);
  assert.equal(b2.Resolver.ownerOf({ tabId: job.tabId }).job, back);
  assert.equal(b2.Resolver.containers.eth, b.Resolver.containers.eth, "its container is kept");
  // It finishes as if nothing happened.
  await Promise.all(b2.fire("downloads.onCreated", { id: 3, url: "https://cdn.test/fresh", totalBytes: 5, cookieStoreId: back && b2.Resolver.containers.eth }));
  await b2.settle();
  assert.equal(engineCalls(b2, "resolved")[0].id, "d1");
  assert.equal(b2.shared.local.recipes["site.test"].steps.length, 1);
});

test("a deadline that passed while the background was away still fires", async () => {
  const b = load();
  await b.settle();
  b.connect();
  await recording(b);
  // Idle for longer than a recording may be, with no timer alive.
  const b2 = load({ shared: b.shared, now: b.clock.now + 6 * 60000 });
  await b2.settle();
  b2.connect();
  await b2.settle();
  assert.equal(engineCalls(b2, "resolveFailed")[0].id, "d1");
  assert.equal(b2.Resolver.jobs.size, 0);
});

test("a job whose window closed while the background was away is not restored", async () => {
  const b = load();
  await b.settle();
  b.connect();
  const job = await recording(b);
  b.shared.windows.delete(job.windowId);
  const b2 = load({ shared: b.shared });
  await b2.settle();
  assert.equal(b2.Resolver.jobs.size, 0);
});

test("the timeouts are deadlines: the fake clock fails an idle recording", async () => {
  const b = load();
  await b.settle();
  b.connect();
  await recording(b);
  await b.advance(5 * 60000 + 1);
  assert.equal(engineCalls(b, "resolveFailed")[0].error, "nobody clicked through the page in time");
});

test("a learn job saves the recipe and reports no link", async () => {
  const b = load();
  await b.settle();
  b.connect();
  const job = await recording(b, { learn: true });
  assert.equal(job.mode, "record");
  message(b, job, { type: "recorded", step: { tag: "a", text: "Download" } });
  await Promise.all(b.fire("downloads.onCreated", { id: 4, url: "https://cdn.test/fresh", totalBytes: 5, cookieStoreId: b.Resolver.containers.eth }));
  await b.settle();
  assert.deepEqual(engineCalls(b, "resolved"), []);
  assert.deepEqual(engineCalls(b, "learnEnded"), [{ id: "d1", iface: "eth" }]);
  assert.ok(b.shared.local.recipes["site.test"]);
  assert.deepEqual(engineCalls(b, "recipeHosts").pop(), { hosts: ["site.test"] });
});

test("a learn job that fails changes nothing for the download", async () => {
  const b = load();
  await b.settle();
  b.connect();
  await recording(b, { learn: true });
  await b.advance(5 * 60000 + 1);
  assert.deepEqual(engineCalls(b, "resolveFailed"), []);
  assert.equal(engineCalls(b, "learnEnded").length, 1);
});

test("a real resolve during a learn job makes it report its link", async () => {
  const b = load();
  await b.settle();
  b.connect();
  const job = await recording(b, { learn: true });
  b.Engine.emit("resolve", { id: "d1", iface: "eth", pageUrl: PAGE });
  await b.settle();
  assert.equal(b.Resolver.jobs.size, 1, "still one job");
  message(b, job, { type: "recorded", step: { tag: "a", text: "Download" } });
  await Promise.all(b.fire("downloads.onCreated", { id: 5, url: "https://cdn.test/fresh", totalBytes: 5, cookieStoreId: b.Resolver.containers.eth }));
  await b.settle();
  assert.equal(engineCalls(b, "resolved")[0].url, "https://cdn.test/fresh");
  assert.deepEqual(engineCalls(b, "learnEnded"), []);
});

test("a learn job for a site that has a recipe by now does nothing", async () => {
  const shared = { local: { recipes: { "site.test": { steps: [], downloadWait: 0 } } }, session: {}, windows: new Map(), nextId: { v: 100 } };
  const b = load({ shared });
  await b.settle();
  b.connect();
  await recording(b, { learn: true });
  assert.equal(b.Resolver.jobs.size, 0);
  assert.ok(!b.calls.some((c) => c[0] === "windows.create"));
  assert.equal(engineCalls(b, "learnEnded").length, 1);
});

test("the engine hears the recipe hosts on connect and when one is forgotten", async () => {
  const shared = { local: { recipes: { "a.test": { steps: [] }, "b.test": { steps: [] } } }, session: {}, windows: new Map(), nextId: { v: 100 } };
  const b = load({ shared });
  await b.settle();
  b.connect();
  await b.settle();
  assert.deepEqual(engineCalls(b, "recipeHosts").pop(), { hosts: ["a.test", "b.test"] });
  await b.Resolver.forgetRecipe("a.test");
  assert.deepEqual(engineCalls(b, "recipeHosts").pop(), { hosts: ["b.test"] });
});

test("without storage.session the resolver still works, in memory", async () => {
  const b = load({ session: false });
  await b.settle();
  b.connect();
  const job = await recording(b);
  assert.equal(job.mode, "record");
});

test("a resolve's container starts with the user's cookies for the landing page, from the store the download came from", async () => {
  const login = { name: "sid", value: "abc", domain: ".site.test", hostOnly: false, path: "/", secure: true, httpOnly: true, session: false, expirationDate: 2e9, sameSite: "lax", firstPartyDomain: "", storeId: "firefox-container-7" };
  const b = load({ cookieJar: { "firefox-container-7": [login], "firefox-default": [{ ...login, value: "other" }] } });
  await b.settle();
  b.connect();
  const job = await recording(b, { cookieStore: "firefox-container-7" });
  const store = b.Resolver.containers.eth;
  const asked = b.calls.find((c) => c[0] === "cookies.getAll" && c[1].url === PAGE);
  assert.equal(asked[1].storeId, "firefox-container-7");
  const sets = b.calls.filter((c) => c[0] === "cookies.set").map((c) => c[1]);
  assert.deepEqual(sets, [{ url: "https://site.test/", name: "sid", value: "abc", path: "/", secure: true, httpOnly: true, storeId: store,
    domain: ".site.test", expirationDate: 2e9, sameSite: "lax" }]);
  // Copied before the page opens.
  const order = b.calls.map((c) => c[0]);
  assert.ok(order.lastIndexOf("cookies.set") < order.indexOf("windows.create"));
  assert.ok(job.windowId);
});

test("a download with no known store uses the normal window's cookies, and a private window's never leave it", async () => {
  const login = { name: "sid", value: "x", domain: "site.test", hostOnly: true, path: "/", secure: false, httpOnly: false, session: true };
  let b = load({ cookieJar: { "firefox-default": [login] } });
  await b.settle();
  b.connect();
  await recording(b);
  const set = b.calls.find((c) => c[0] === "cookies.set")[1];
  assert.equal(set.url, "http://site.test/");
  assert.equal(set.domain, undefined); // host-only stays host-only
  assert.equal(set.expirationDate, undefined); // a session cookie stays one

  b = load({ cookieJar: { "firefox-private": [login] } });
  await b.settle();
  b.connect();
  await recording(b, { cookieStore: "firefox-private" });
  assert.equal(b.calls.filter((c) => c[0] === "cookies.set").length, 0);
});

test("after the user quits the engine from its tray, the extension doesn't start it again until asked", async () => {
  const b = load();
  await b.settle();
  const ports = () => b.listeners["port.onMessage"].length; // one per connectNative
  const before = ports();
  b.fire("port.onMessage", { event: "quit" });
  b.fire("port.onDisconnect", {});
  await b.advance(60000);
  assert.equal(ports(), before);
  assert.equal(b.Engine.quit, true);
  assert.equal(b.Engine.lastError, "");
  await b.fire("runtime.onMessage", { type: "startEngine" }, {})[0];
  assert.equal(ports(), before + 1);
  assert.equal(b.Engine.quit, false);
});

test("a lost engine it didn't quit is reconnected", async () => {
  const b = load();
  await b.settle();
  const before = b.listeners["port.onMessage"].length;
  b.fire("port.onDisconnect", {});
  await b.advance(2000);
  assert.equal(b.listeners["port.onMessage"].length, before + 1);
});
