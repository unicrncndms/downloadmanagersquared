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
//   startEngine()                    start the engine again after the user quit it (extension only)
//   version                          this UI's version, compared with the engine's
//   userAgent                        sent with "Add download"; null leaves it to the engine
//   recipesHere                      whether learned sites can be shown and forgotten here
//   openDownloads()                  back to the downloads (only in the Downloads window)

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
    startEngine: () => browser.runtime.sendMessage({ type: "startEngine" }),
    version: browser.runtime.getManifest().version,
    userAgent: navigator.userAgent,
    recipesHere: true,
  };
}

// In the Downloads window (WebView2 in dms-engine.exe): the page speaks the
// engine's JSON lines itself, through bridge (window.dmsWindow, set up by the
// window before the page loads), over the window's in-memory pipe. Requests
// go through lib/rpc.js, as in background/engine.js. The window
// never says hello, so it gets no resolves and leaves the browser's user agent
// alone.
function windowHost(win, bridge) {
  const rpc = rpcClient((m) => bridge.send(JSON.stringify(m)), (f, ms) => win.setTimeout(f, ms));
  const subscribers = new Set();
  const latest = {
    connected: false, error: "", notInstalled: false, engineVersion: "",
    state: null, settings: null, autostart: false, recipes: null,
  };
  let connecting = null;
  const emit = () => {
    for (const fn of subscribers) {
      try { fn({ ...latest }); } catch (e) { report(e); }
    }
  };
  // DevTools is off in the window: a page error goes to engine.log instead.
  const report = (e) => {
    console.error(e);
    if (bridge.log) bridge.log(String((e && e.stack) || e));
  };
  if (win.addEventListener) win.addEventListener("error", (ev) => report(ev.error || ev.message));

  bridge.onLine = (line) => {
    let msg;
    try { msg = JSON.parse(line); } catch { return; }
    if (!msg || typeof msg !== "object") return;
    if (msg.event) {
      if (msg.event !== "state" && msg.event !== "settings") return;
      latest[msg.event] = msg.data;
      if (latest.connected) emit();
      return;
    }
    rpc.answer(msg);
  };

  function call(method, params) {
    const p = rpc.call(method, params);
    if (method === "setAutostart") p.then(() => { latest.autostart = !!(params && params.on); }, () => {});
    return p;
  }

  function connect() {
    connecting = Promise.all(["getState", "getSettings", "getAutostart", "version"].map((m) => call(m)))
      .then(([state, settings, autostart, version]) => {
        Object.assign(latest, { state, settings, autostart: !!autostart, engineVersion: version || "", connected: true, error: "" });
      })
      .catch((e) => {
        Object.assign(latest, { connected: false, error: e.message });
        connecting = null; // the next subscribe tries again
      })
      .then(emit);
  }

  return {
    call,
    subscribe(fn) {
      subscribers.add(fn);
      // Nothing before the engine's first answer: the page would draw
      // "Engine offline" for a moment each time it opens.
      if (latest.connected || latest.error) fn({ ...latest });
      if (!connecting) connect();
      return () => subscribers.delete(fn);
    },
    openSettings: (hash) => { win.location.href = "settings.html" + (hash || ""); },
    openDownloads: () => { win.location.href = "popup.html?window"; },
    openUrl: (url) => bridge.openUrl(url),
    get version() { return latest.engineVersion; }, // the window is part of the engine
    userAgent: null,
    recipesHere: false,
  };
}

// Where the pages run: a Host already set (the harness sets its own) is kept.
function pickHost(win) {
  if (win.Host) return win.Host;
  if (win.chrome && win.chrome.webview && win.dmsWindow) return windowHost(win, win.dmsWindow);
  if (win.browser && win.browser.runtime) return extensionHost(win.browser, win.navigator || {});
  return null;
}

if (typeof window !== "undefined") window.Host = pickHost(window);
if (typeof module !== "undefined") module.exports = { pickHost };
