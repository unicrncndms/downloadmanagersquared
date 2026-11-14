"use strict";

// Bridges the downloads popup and the settings page to the engine.
browser.runtime.onConnect.addListener((port) => {
  if (port.name !== "ui") return;
  const send = () => port.postMessage({
    connected: Engine.connected,
    error: Engine.lastError,
    notInstalled: Engine.notInstalled,
    quit: Engine.quit,
    engineVersion: Engine.version,
    state: Engine.state,
    settings: Engine.settings,
    autostart: Engine.autostart,
    recipes: Resolver.recipes,
  });
  // The downloads list shows what needs the user: that clears the "!" for it.
  if (String(port.sender && port.sender.url || "").endsWith("/popup.html")) {
    seen = seenAfter(Engine.state && Engine.state.downloads, seen, true);
    paintBadge(Engine.state && Engine.state.downloads);
  }
  send();
  const offs = ["state", "settings", "connection"].map((ev) => Engine.on(ev, send));
  port.onDisconnect.addListener(() => offs.forEach((off) => off()));
});

// Only this extension's own pages (ui/) may ask for these; content scripts run in web pages.
const fromUi = (sender) => !!sender && sender.id === browser.runtime.id && String(sender.url || "").startsWith(browser.runtime.getURL("ui/"));
const PRIVILEGED = new Set(["call", "startEngine", "forgetRecipe", "openSettings"]);

browser.runtime.onMessage.addListener((msg, sender) => {
  if (!msg || msg.resolver) return undefined;
  if (PRIVILEGED.has(msg.type) && !fromUi(sender)) return undefined;
  if (msg.type === "magnet") return Capture.takeMagnet(msg.url, sender.tab);
  if (msg.type === "call") {
    const p = Engine.call(msg.method, msg.params);
    if (msg.method === "setAutostart") p.then(() => { Engine.autostart = msg.params.on; }, () => {}); // the caller gets the rejection
    return p;
  }
  if (msg.type === "startEngine") return Engine.start();
  if (msg.type === "forgetRecipe") return Resolver.forgetRecipe(msg.host);
  if (msg.type === "openSettings") return openSettings(msg.hash || "");
  return undefined;
});

// The toolbar button shows what is running, or "!" when a download needs you and the list has not shown it; it follows the engine, so it outlives the popup.
let badge = "";
let seen = new Set(); // ids that need the user and that the downloads list has shown (in memory: the background page is persistent)
function paintBadge(downloads) {
  const b = badgeOf(downloads, seen);
  if (b.text + b.color === badge) return;
  badge = b.text + b.color;
  browser.browserAction.setBadgeBackgroundColor({ color: b.color });
  browser.browserAction.setBadgeText({ text: b.text });
}
Engine.on("state", (st) => {
  const ds = st && st.downloads;
  seen = seenAfter(ds, seen, false);
  paintBadge(ds);
});
Engine.on("connection", ({ connected }) => { if (!connected) { badge = ""; browser.browserAction.setBadgeText({ text: "" }); } });

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

// The accent follows Firefox's theme: the engine keeps its color, so the
// Downloads window (which can't read the theme) gets it too.
async function sendThemeAccent() {
  if (!Engine.connected || !Engine.settings) return;
  const c = themeAccent(await browser.theme.getCurrent().catch(() => null));
  if (c !== (Engine.settings.themeAccent || "")) await Engine.call("setSettings", { themeAccent: c }).catch(() => {});
}
Engine.on("connection", ({ connected }) => { if (connected) sendThemeAccent(); });
browser.theme.onUpdated.addListener(() => { sendThemeAccent(); });

// The engine shows the downloads and settings in its own Downloads window. It
// asks this extension only when that window can't be created (WebView2
// missing): this listener is all that's left for that fallback.
Engine.on("openManager", ({ hash } = {}) => {
  if (hash) return openSettings(hash === "#settings" ? "" : hash);
  return browser.windows.create({ type: "popup", url: DOWNLOADS_URL + "?window", width: 520, height: 680 });
});

// Once, on install: a page that says what DMS² takes over and what its prompts are for.
browser.runtime.onInstalled.addListener(({ reason }) => {
  if (reason === "install") browser.tabs.create({ url: browser.runtime.getURL("ui/welcome.html") });
});

// Every listener is registered now, before anything is awaited.
SentCookies.init();
Resolver.init();
Capture.init();
Engine.connect();
