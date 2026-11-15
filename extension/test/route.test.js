"use strict";

const test = require("node:test");
const assert = require("node:assert");
const { containerPort, ownerOf, startChoice, staleContainers } = require("../lib/route.js");
const { pageMenuFits } = require("../lib/capture-rules.js");

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

test("ownerOf: a job's tab, then its window, then its container; the user's own is null", () => {
  const rec = { iface: "eth", windowId: 7, mode: "record", done: false };
  const done = { iface: "eth", windowId: 8, mode: "replay", done: true };
  const st = { tabJobs: new Map([[1, rec]]), jobs: new Map([["a", rec], ["b", done]]), ifaceByStore: { box: "eth", idle: "wifi" } };
  assert.equal(ownerOf(st, { tabId: 1 }).job, rec);
  assert.equal(ownerOf(st, { tabId: -1, windowId: 8 }).job, done);
  assert.equal(ownerOf(st, { tabId: 9, cookieStoreId: "box" }).job, rec);       // no tab of its own: the container's running job
  assert.deepEqual(ownerOf(st, { cookieStoreId: "idle" }), { job: null, iface: "wifi" }); // a container with no resolve
  assert.equal(ownerOf(st, { tabId: 9, cookieStoreId: "firefox-default" }), null);
  assert.equal(ownerOf(st, {}), null);
});

test("startChoice: replay a known site, wait behind a recorder, else record; a learn job on a known site ends", () => {
  const job = { host: "a.test", learn: false, mode: "new" };
  const other = (mode, host = "a.test") => ({ host, mode });
  assert.equal(startChoice(job, true, [job]), "replay");
  assert.equal(startChoice({ ...job, learn: true }, true, [job]), "endLearn");
  assert.equal(startChoice(job, false, [job]), "record");
  assert.equal(startChoice(job, false, [job, other("record")]), "waiting");
  assert.equal(startChoice(job, false, [job, other("record", "b.test"), other("waiting")]), "record");
  // at most two windows per host; a finished one frees a place
  assert.equal(startChoice(job, true, [job, other("replay"), other("replay")]), "waiting");
  assert.equal(startChoice(job, true, [job, other("replay"), other("replay", "b.test"), { ...other("replay"), done: true }]), "replay");
  assert.equal(startChoice({ ...job, learn: true }, true, [job, other("replay"), other("replay")]), "endLearn");
});

test("staleContainers: stored or DMS²-named ones not kept by a carrying-on job", () => {
  const listed = [{ name: "DMS² · Eth", cookieStoreId: "c1" }, { name: "DMS² · Wi-Fi", cookieStoreId: "c2" }, { name: "Work", cookieStoreId: "c3" }];
  assert.deepEqual(staleContainers({ eth: "c1", wifi: "c9" }, ["c1"], listed, "DMS² · ").sort(), ["c2", "c9"]);
  assert.deepEqual(staleContainers(undefined, [], [], "DMS² · "), []);
});

test("pageMenuFits: a file's address or a learned site's page", () => {
  assert.equal(pageMenuFits("https://cdn.test/big.zip", {}), true);
  assert.equal(pageMenuFits("https://site.test/get/1", { "site.test": {} }), true);
  assert.equal(pageMenuFits("https://news.test/story", { "site.test": {} }), false);
  assert.equal(pageMenuFits("", {}), false);
});
