"use strict";

// Host: the one way the downloads and settings pages reach their surroundings.
// The pages never use browser.* themselves; the same pages run in the
// extension, in the engine's Downloads window and in the UI harness.
//
//   call(method, params) → Promise   an engine request
//   subscribe(fn) → stop()           fn gets { connected, error, notInstalled,
//                                    engineVersion, state, settings, autostart, recipes }
//   openSettings(hash)               show the settings page (hash like "#pia")
//   openUrl(url)                     a link that leaves DMS²
//   forgetRecipe(host)               drop a learned site
//   version                          this UI's version, compared with the engine's
//   userAgent                        sent with "Add download"; null leaves it to the engine
//   recipesHere                      whether learned sites can be shown and forgotten here

// In the extension: the background page talks to the engine.
function extensionHost(browser, navigator) {
  return {
    call: (method, params) => browser.runtime.sendMessage({ type: "call", method, params }),
    subscribe(fn) {
      const port = browser.runtime.connect({ name: "ui" });
      port.onMessage.addListener(fn);
      return () => port.disconnect();
    },
    openSettings: (hash) => browser.runtime.sendMessage({ type: "openSettings", hash: hash || "" }),
    openUrl: (url) => browser.tabs.create({ url }),
    forgetRecipe: (host) => browser.runtime.sendMessage({ type: "forgetRecipe", host }),
    version: browser.runtime.getManifest().version,
    userAgent: navigator.userAgent,
    recipesHere: true,
  };
}

// Where the pages run: a Host already set (the harness sets its own) is kept.
function pickHost(win) {
  if (win.Host) return win.Host;
  // The Downloads window (WebView2 in dms-engine.exe) goes here: when
  // win.chrome.webview is there, return its adapter, which speaks the
  // engine's JSON lines over the window's in-memory pipe.
  if (win.browser && win.browser.runtime) return extensionHost(win.browser, win.navigator || {});
  return null;
}

if (typeof window !== "undefined") window.Host = pickHost(window);
if (typeof module !== "undefined") module.exports = { pickHost };
