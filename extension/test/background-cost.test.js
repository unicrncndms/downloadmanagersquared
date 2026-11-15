"use strict";

// What the background spends per navigation and per resolve: one request-header
// listener, capture leaving ordinary responses early, cookies copied in
// parallel, tabs judged on events, and the resolver's fence for heavy requests.

const test = require("node:test");
const assert = require("node:assert");
const { load } = require("./fake-background.js");

const PAGE = "https://site.test/get/1";

async function recording(b, extra = {}) {
  b.Engine.emit("resolve", { id: "d1", iface: "eth", ifaceName: "Ethernet", pageUrl: PAGE, ...extra });
  await b.settle();
  return b.Resolver.jobs.get("d1|eth");
}

const engineCalls = (b, method) => b.engine.filter((c) => c.method === method).map((c) => c.params);

test("one request-header listener per document load keeps both the Cookie and the Referer", async () => {
  const b = load();
  await b.settle();
  assert.equal((b.listeners["webRequest.onBeforeSendHeaders"] || []).length, 0);
  const headers = [{ name: "Cookie", value: "a=1" }, { name: "Referer", value: "https://from.test/p" }];
  b.fire("webRequest.onSendHeaders", { requestId: "r1", url: "https://site.test/p", type: "main_frame", requestHeaders: headers });
  assert.equal(b.Capture.referers.get("https://site.test/p"), "https://from.test/p");
  assert.equal(b.get("SentCookies").take("r1"), "a=1");
  // An iframe's Referer is not a page's: only its Cookie is kept.
  b.fire("webRequest.onSendHeaders", { requestId: "r2", url: "https://ads.test/f", type: "sub_frame", requestHeaders: headers });
  assert.equal(b.Capture.referers.has("https://ads.test/f"), false);
  assert.equal(b.get("SentCookies").take("r2"), "a=1");
});

test("only a 200 or 206 response is capture's: a redirect or an error with a big file's headers is left alone", async () => {
  const b = load();
  await b.settle();
  b.connect();
  const resp = (statusCode) => ({
    requestId: "x", url: "https://cdn.test/big.zip", method: "GET", statusCode, tabId: 5, type: "main_frame",
    responseHeaders: [{ name: "Content-Type", value: "application/zip" }, { name: "Content-Length", value: "900000000" }],
  });
  const fire = async (d) => (await Promise.all(b.fire("webRequest.onHeadersReceived", d)))[0];
  assert.deepEqual(await fire(resp(302)), {});
  assert.deepEqual(await fire(resp(404)), {});
  assert.deepEqual(await fire({ ...resp(200), responseHeaders: [{ name: "Content-Type", value: "text/html; charset=utf-8" }] }), {});
  assert.equal(engineCalls(b, "add").length, 0);
  assert.deepEqual(await fire(resp(200)), { cancel: true });
  assert.equal(engineCalls(b, "add").length, 1);
});

test("the magnet content script waits for the page to be idle but stays in every frame", () => {
  const m = require("../manifest.json").content_scripts.find((c) => c.js.includes("content/magnet.js"));
  assert.equal(m.run_at, "document_idle");
  assert.equal(m.all_frames, true);
});

test("a resolve copies its cookies in parallel", async () => {
  const login = (n) => ({ name: "c" + n, value: "v", domain: "site.test", hostOnly: true, path: "/", secure: true, httpOnly: false, session: true });
  const b = load({ cookieJar: { "firefox-default": Array.from({ length: 40 }, (_, i) => login(i)) } });
  let running = 0, peak = 0;
  b.browser.cookies.set = async () => { peak = Math.max(peak, ++running); await Promise.resolve(); running--; };
  await b.settle();
  assert.equal(await b.get("copyCookies")(PAGE, "firefox-default", "store-1"), 40);
  assert.equal(peak, 40);
  // A refused one is not counted and does not stop the rest.
  b.browser.cookies.set = async (c) => { if (c.name === "c3") throw new Error("no"); };
  assert.equal(await b.get("copyCookies")(PAGE, "firefox-default", "store-1"), 39);
});

test("a tab a replayed click opened is judged on its updates and once when its moment is up, not polled", async () => {
  const b = load();
  await b.settle();
  b.connect();
  const job = await recording(b);
  job.mode = "replay";
  let gets = 0;
  let url = "";
  b.browser.tabs.get = async (id) => { gets++; return { id, url }; };
  b.fire("tabs.onCreated", { id: 900, openerTabId: job.tabId, windowId: job.windowId });
  await b.settle();
  await b.advance(20000);
  assert.ok(gets <= 3, `tabs.get ${gets} times`);
  assert.equal(b.calls.filter((c) => c[0] === "tabs.remove" && c[1] === 900).length, 1); // another site, no download: a pop-up ad
  assert.equal(b.Resolver.newTabs.size, 0);

  // A tab that turns out to be the same site is followed at once.
  url = "https://site.test/next";
  b.fire("tabs.onCreated", { id: 901, openerTabId: job.tabId, windowId: job.windowId });
  await b.settle();
  assert.equal(job.tabId, 901);
  assert.equal(b.Resolver.newTabs.size, 0);
});

test("a click with no effect yet is judged by one timer for the wait that is left, not re-armed", async () => {
  const b = load();
  await b.settle();
  b.connect();
  const job = await recording(b);
  job.mode = "replay";
  b.Resolver.steps = () => [{ wait: 0 }];
  await Promise.all(b.fire("runtime.onMessage", { resolver: true, type: "clicked", index: 0 }, { tab: { id: job.tabId, url: PAGE } }));
  const at = job.clicked.at;
  await b.advance(500);
  b.Resolver.judgeClick(job);
  assert.equal(job.deadlines.effect.at, at + 2000);
});
