"use strict";

// A browser for the UI pages outside Firefox: the background's port feeds them
// the fixture from dev/fixtures.js, and what they send is kept in window.calls.

window.calls = [];
window.errs = [];
window.onerror = (msg, src, line) => { window.errs.push(`${msg} @ ${src}:${line}`); };

(() => {
  const listeners = [];
  const store = {};
  const changed = [];

  // Hands every open port a message, as the background does on each change.
  window.feed = (m) => { for (const f of listeners) f(m); };

  window.browser = {
    runtime: {
      connect: () => {
        const port = {
          onMessage: { addListener: (f) => { listeners.push(f); setTimeout(() => f(window.fixture()), 0); } },
          onDisconnect: { addListener() {} },
          postMessage() {},
        };
        return port;
      },
      sendMessage: (m) => { window.calls.push(m); return Promise.resolve(); },
      getURL: (p) => "/" + p.replace(/^\//, ""),
      getManifest: () => ({ version: window.fixtureExtensionVersion || "0.3.0" }),
    },
    storage: {
      local: {
        get: async (keys) => {
          const out = {};
          for (const k of [].concat(keys || Object.keys(store))) if (k in store) out[k] = store[k];
          return out;
        },
        set: async (o) => {
          Object.assign(store, o);
          for (const f of changed) f(Object.fromEntries(Object.entries(o).map(([k, v]) => [k, { newValue: v }])), "local");
        },
      },
      onChanged: { addListener: (f) => changed.push(f) },
    },
    tabs: { create: async (o) => { window.calls.push({ tabsCreate: o }); } },
  };
})();
