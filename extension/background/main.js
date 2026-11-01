"use strict";

// Bridges the downloads popup and the settings page to the engine.
browser.runtime.onConnect.addListener((port) => {
  if (port.name !== "ui") return;
  const send = () => port.postMessage({
    connected: Engine.connected,
    error: Engine.lastError,
    state: Engine.state,
    settings: Engine.settings,
    autostart: Engine.autostart,
    recipes: Resolver.recipes,
  });
  send();
  const offs = ["state", "settings", "connection"].map((ev) => Engine.on(ev, send));
  port.onDisconnect.addListener(() => offs.forEach((off) => off()));
});

browser.runtime.onMessage.addListener((msg) => {
  if (!msg || msg.resolver) return undefined;
  if (msg.type === "call") {
    const p = Engine.call(msg.method, msg.params);
    if (msg.method === "setAutostart") p.then(() => { Engine.autostart = msg.params.on; });
    return p;
  }
  if (msg.type === "forgetRecipe") return Resolver.forgetRecipe(msg.host);
  if (msg.type === "openSettings") return openSettings(msg.hash || "");
  return undefined;
});

const SETTINGS_URL = browser.runtime.getURL("ui/settings.html");
const DOWNLOADS_URL = browser.runtime.getURL("ui/popup.html");
let downloadsWindow = null;

// The settings page lives in a tab; reuse it if it's already open.
async function openSettings(hash) {
  const url = SETTINGS_URL + (hash || "");
  const tab = (await browser.tabs.query({})).find((t) => (t.url || "").startsWith(SETTINGS_URL));
  if (tab) {
    await browser.tabs.update(tab.id, { active: true, url });
    await browser.windows.update(tab.windowId, { focused: true });
    return;
  }
  const win = await browser.windows.getLastFocused().catch(() => null);
  if (win && win.type === "normal") await browser.tabs.create({ url, windowId: win.id });
  else await browser.windows.create({ url });
}

// Downloads are shown in the toolbar popup. Firefox only opens that on a user's
// click, so when asked from the tray it falls back to a small popup window.
async function openDownloads() {
  try {
    await browser.browserAction.openPopup();
    return;
  } catch (e) { /* not allowed without a click here */ }
  if (downloadsWindow !== null) {
    try {
      await browser.windows.update(downloadsWindow, { focused: true });
      return;
    } catch (e) {
      downloadsWindow = null;
    }
  }
  const w = await browser.windows.create({ type: "popup", url: DOWNLOADS_URL + "?window", width: 520, height: 680 });
  downloadsWindow = w.id;
}
browser.windows.onRemoved.addListener((id) => {
  if (id === downloadsWindow) downloadsWindow = null;
});

Engine.on("openManager", ({ hash } = {}) => (hash === "#settings" ? openSettings("") : openDownloads()));

(async () => {
  await Resolver.init();
  Capture.init();
  Engine.connect();
})();
