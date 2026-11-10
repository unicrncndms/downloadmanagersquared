"use strict";

// Loads the background scripts, in manifest order, against a fake browser the
// test drives: events are fired by name ("downloads.onCreated"), and what the
// scripts ask of the engine lands in `engine` instead of a native port. Time
// is a fake clock; `advance(ms)` runs the timeouts that come due.

const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const root = path.join(__dirname, "..");
const manifest = JSON.parse(fs.readFileSync(path.join(root, "manifest.json"), "utf8"));

const clone = (v) => (v === undefined ? v : JSON.parse(JSON.stringify(v)));

function storageArea(data) {
  return {
    data,
    async get(keys) {
      const list = keys === undefined || keys === null ? Object.keys(data) : [].concat(keys);
      const out = {};
      for (const k of list) if (k in data) out[k] = clone(data[k]);
      return out;
    },
    async set(o) { Object.assign(data, clone(o)); },
    async remove(keys) { for (const k of [].concat(keys)) delete data[k]; },
  };
}

// shared: what outlives a restart of the background (storage, open windows).
// cookieJar: the cookies each store holds, by store id; cookies.getAll hands
// out the asked store's (the real browser also filters them by URL).
function load({ shared = { local: {}, session: {}, windows: new Map(), nextId: { v: 100 } }, scripts, session = true, now = 1000000, cookieJar = {} } = {}) {
  const listeners = {};
  const calls = [];
  const engine = [];
  const clock = { now, timers: [] };
  const ev = (name) => ({
    addListener: (f) => (listeners[name] = listeners[name] || []).push(f),
    removeListener() {},
    hasListener: () => false,
  });
  const rec = (name, result) => (...args) => {
    calls.push([name, ...args]);
    return Promise.resolve(typeof result === "function" ? result(...args) : result);
  };
  const id = () => shared.nextId.v++;
  const stores = [];

  const browser = {
    runtime: {
      id: "dms@test",
      getURL: (p) => "moz-extension://x/" + p,
      connectNative: () => ({ onMessage: ev("port.onMessage"), onDisconnect: ev("port.onDisconnect"), postMessage() {}, disconnect() {} }),
      onMessage: ev("runtime.onMessage"),
      onConnect: ev("runtime.onConnect"),
    },
    storage: { local: storageArea(shared.local), session: session ? storageArea(shared.session) : undefined },
    proxy: { onRequest: ev("proxy.onRequest"), onError: ev("proxy.onError") },
    webRequest: {
      onBeforeRequest: ev("webRequest.onBeforeRequest"), onHeadersReceived: ev("webRequest.onHeadersReceived"),
      onSendHeaders: ev("webRequest.onSendHeaders"), onErrorOccurred: ev("webRequest.onErrorOccurred"),
      onBeforeSendHeaders: ev("webRequest.onBeforeSendHeaders"),
    },
    webNavigation: { onCommitted: ev("webNavigation.onCommitted"), onHistoryStateUpdated: ev("webNavigation.onHistoryStateUpdated") },
    downloads: { onCreated: ev("downloads.onCreated"), cancel: rec("downloads.cancel"), erase: rec("downloads.erase"), download: rec("downloads.download") },
    cookies: { getAll: rec("cookies.getAll", (q) => (cookieJar[q.storeId || "firefox-default"] || []).slice()), set: rec("cookies.set") },
    notifications: { create: rec("notifications.create") },
    theme: { getCurrent: rec("theme.getCurrent", () => shared.theme || { colors: null }), onUpdated: ev("theme.onUpdated") },
    contextMenus: { create: rec("contextMenus.create"), onClicked: ev("contextMenus.onClicked") },
    contextualIdentities: {
      create: rec("contextualIdentities.create", (o) => {
        const c = { ...o, cookieStoreId: "firefox-container-" + id() };
        stores.push(c);
        return c;
      }),
      query: rec("contextualIdentities.query", () => stores.slice()),
      remove: rec("contextualIdentities.remove"),
      onRemoved: ev("contextualIdentities.onRemoved"),
    },
    windows: {
      create: rec("windows.create", (o) => {
        const w = { id: id(), state: o.state || "normal", tabs: [{ id: id(), url: o.url }] };
        shared.windows.set(w.id, w);
        return w;
      }),
      get: (wid) => (shared.windows.has(wid) ? Promise.resolve(shared.windows.get(wid)) : Promise.reject(new Error("no window"))),
      update: rec("windows.update"),
      remove: rec("windows.remove", (wid) => { shared.windows.delete(wid); }),
      getLastFocused: rec("windows.getLastFocused", null),
      onRemoved: ev("windows.onRemoved"),
    },
    tabs: {
      query: rec("tabs.query", []),
      get: (tid) => Promise.resolve({ id: tid, url: "" }),
      remove: rec("tabs.remove"), update: rec("tabs.update"), create: rec("tabs.create"),
      sendMessage: rec("tabs.sendMessage"), executeScript: rec("tabs.executeScript"),
      onUpdated: ev("tabs.onUpdated"), onCreated: ev("tabs.onCreated"), onRemoved: ev("tabs.onRemoved"),
    },
  };

  const ctx = vm.createContext({
    browser, console: { log() {}, warn() {}, error() {}, info() {} }, URL, URLSearchParams, Promise,
    navigator: { userAgent: "test" }, screen: { availWidth: 1920, availHeight: 1080, availLeft: 0, availTop: 0 },
    Date: { now: () => clock.now },
    setTimeout: (f, ms) => { clock.timers.push({ at: clock.now + (ms || 0), f }); return clock.timers.length; },
    clearTimeout: (n) => { if (clock.timers[n - 1]) clock.timers[n - 1].f = null; },
  });
  for (const file of scripts || manifest.background.scripts) {
    vm.runInContext(fs.readFileSync(path.join(root, file), "utf8"), ctx, { filename: file });
  }
  const get = (name) => vm.runInContext(name, ctx);

  // The engine: connected, with one interface per entry of ifaces.
  const E = get("Engine");
  E.pending.clear(); // the real hello, never answered here
  E.call = (method, params) => {
    engine.push({ method, params: clone(params) });
    return Promise.resolve(method === "add" ? { id: "d" + engine.length } : {});
  };
  const connect = (ifaces = [{ id: "eth", present: true, socksPort: 5001 }], settings = { thresholdBytes: 100e6 }) => {
    E.settings = settings;
    E.state = { interfaces: ifaces };
    E.setConnected(true, "");
  };

  const fire = (name, ...args) => (listeners[name] || []).map((f) => f(...args));
  const settle = () => new Promise((r) => setImmediate(r)).then(() => new Promise((r) => setImmediate(r)));
  const advance = async (ms) => {
    const until = clock.now + ms;
    for (;;) {
      const due = clock.timers.filter((t) => t.f && t.at <= until).sort((a, b) => a.at - b.at)[0];
      if (!due) break;
      clock.now = Math.max(clock.now, due.at);
      const f = due.f;
      due.f = null;
      f();
      await settle();
    }
    clock.now = until;
  };

  return { browser, listeners, calls, engine, clock, shared, fire, settle, advance, get, connect, Engine: E, Resolver: get("Resolver"), Capture: get("Capture") };
}

module.exports = { load };
