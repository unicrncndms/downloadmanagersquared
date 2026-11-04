"use strict";

// Bridges the downloads popup and the settings page to the engine.
browser.runtime.onConnect.addListener((port) => {
  if (port.name !== "ui") return;
  const send = () => port.postMessage({
    connected: Engine.connected,
    error: Engine.lastError,
    notInstalled: Engine.notInstalled,
    engineVersion: Engine.version,
    state: Engine.state,
    settings: Engine.settings,
    autostart: Engine.autostart,
    recipes: Resolver.recipes,
  });
  send();
  const offs = ["state", "settings", "connection"].map((ev) => Engine.on(ev, send));
  port.onDisconnect.addListener(() => offs.forEach((off) => off()));
});

browser.runtime.onMessage.addListener((msg, sender) => {
  if (!msg || msg.resolver) return undefined;
  if (msg.type === "magnet") return Capture.takeMagnet(msg.url, sender.tab);
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

// Once after upgrading: the VPN provider choice moves from browser.storage to the engine.
Engine.on("connection", ({ connected }) => {
  if (connected) handOverProvider(browser.storage.local, Engine.settings, (m, p) => Engine.call(m, p)).catch(() => {});
});

// The engine shows the downloads and settings in its own Downloads window. It
// asks this extension only when that window can't be created (WebView2
// missing): this listener is all that's left for that fallback.
Engine.on("openManager", ({ hash } = {}) => {
  if (hash) return openSettings(hash === "#settings" ? "" : hash);
  return browser.windows.create({ type: "popup", url: DOWNLOADS_URL + "?window", width: 520, height: 680 });
});

(async () => {
  SentCookies.init();
  await Resolver.init();
  Capture.init();
  Engine.connect();
})();
