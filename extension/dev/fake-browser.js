"use strict";

// The harness's Host (see ui/host.js), set before host.js loads so it is kept:
// the pages are fed the fixture from dev/fixtures.js, and what they ask for is
// kept in window.calls. A fixture's `host` overrides version, userAgent and
// recipesHere, and openDownloads (e.g. the Downloads window's).

window.calls = [];
window.errs = [];
window.onerror = (msg, src, line) => { window.errs.push(`${msg} @ ${src}:${line}`); };

(() => {
  const listeners = [];
  const record = (m) => { window.calls.push(m); return Promise.resolve(); };
  const hostFixture = () => Object.assign({ version: "0.3.0", userAgent: navigator.userAgent, recipesHere: true }, window.fixture().host);

  // Hands every subscriber a message, as the background does on each change.
  window.feed = (m) => { for (const f of listeners) f(m); };

  window.Host = {
    // A fixture's `files` answers torrentFiles, by download id; its `lines` checkLines.
    call: (method, params) => record({ call: method, params })
      .then(() => method === "torrentFiles" ? (window.fixture().files || {})[params.id] || []
        : method === "checkLines" ? window.fixture().lines || []
        : method === "downloadLog" ? (window.fixture().logs || {})[params.id] || [] : undefined),
    subscribe(fn) {
      listeners.push(fn);
      setTimeout(() => fn(window.fixture()), 0);
      return () => listeners.splice(listeners.indexOf(fn), 1);
    },
    openSettings: (hash) => record({ openSettings: hash }),
    openUrl: (url) => record({ openUrl: url }),
    // Only in the Downloads window.
    get openDownloads() { return hostFixture().openDownloads ? () => record({ openDownloads: true }) : undefined; },
    forgetRecipe: (host) => record({ forgetRecipe: host }),
    startEngine: () => record({ startEngine: true }),
    get version() { return hostFixture().version; },
    get userAgent() { return hostFixture().userAgent; },
    get recipesHere() { return hostFixture().recipesHere; },
  };
})();
