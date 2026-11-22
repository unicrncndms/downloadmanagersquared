"use strict";

// Shared by the downloads popup and the settings page. They reach the engine
// and their surroundings only through Host (host.js).

// A page may set onCall(method, params, answer) to see its own requests go
// out: the downloads page shows what they will do at once (popup.js).
let onCall = null;

function call(method, params) {
  const answer = Host.call(method, params);
  if (onCall) onCall(method, params, answer);
  return answer;
}

function connectUI(onUpdate) {
  const draw = (m) => { applyAccent(m.settings); applyTheme(m.settings); if (m.settings) setSpeedUnit(m.settings.speedUnit); onUpdate(m); };
  // A hidden page (a minimized Downloads window, another tab) draws nothing:
  // it keeps the latest news and draws it when shown again.
  let held = null;
  document.addEventListener("visibilitychange", () => {
    if (document.hidden || !held) return;
    const m = held;
    held = null;
    draw(m);
  });
  return Host.subscribe((m) => { if (document.hidden) held = m; else draw(m); });
}

// Sets the accent tokens (lib/accent.js) from the engine's settings; without
// settings (engine offline) the last ones stay.
function applyAccent(settings) {
  if (!settings) return;
  let style = document.getElementById("accent");
  if (!style) style = document.head.appendChild(Object.assign(document.createElement("style"), { id: "accent" }));
  const css = accentCSS(pageAccent(settings));
  if (style.textContent !== css) style.textContent = css;
}

// The theme choice: light or dark overrides the system's (ui.css, data-theme); "" follows it.
// And whether the surfaces take a tint of the accent (data-tint).
function applyTheme(settings) {
  if (!settings) return;
  const t = settings.theme === "light" || settings.theme === "dark" ? settings.theme : "";
  if (t) document.documentElement.dataset.theme = t;
  else delete document.documentElement.dataset.theme;
  document.documentElement.toggleAttribute("data-tint", !!settings.tintBackground);
}

// An (i)'s note (fixed, ui.css) is placed by the (i): to the right, or to the left
// when the right has less room; below, or above when there is less room below than
// a few lines of note need.
function sideInfo(e) {
  const i = e.target.closest && e.target.closest(".info");
  if (!i) return;
  const r = i.getBoundingClientRect();
  const w = document.documentElement.clientWidth, h = document.documentElement.clientHeight;
  i.classList.toggle("left", w - r.left < r.right);
  i.classList.toggle("up", h - r.bottom < 140 && r.top > h - r.bottom);
  i.style.setProperty("--tip-l", `${r.left - 8}px`);
  i.style.setProperty("--tip-r", `${w - r.right - 8}px`);
  i.style.setProperty("--tip-t", `${r.bottom + 6}px`);
  i.style.setProperty("--tip-b", `${h - r.top + 6}px`);
}
addEventListener("pointerover", sideInfo);
addEventListener("focusin", sideInfo);

// A link that leaves DMS²: Host opens it (a new tab, or the default browser).
function link(url, text) {
  // Only https leaves DMS²: anything else (from a tampered state) is shown as text.
  if (!/^https:\/\//.test(url)) return el("span", {}, text);
  return el("a", { href: url, onclick: (e) => { e.preventDefault(); Host.openUrl(url); } }, text);
}

// Where the engine and the extension are released (build.ps1 -Repo sets the repo).
const RELEASES_URL = "https://github.com/unicrncndms/downloadmanagersquared/releases/latest";

// The extension's page on addons.mozilla.org (must match the listing's slug; install.AMOListingURL in the engine).
const AMO_URL = "https://addons.mozilla.org/firefox/addon/downloadmanagersquared/";

// The engine zip as one stable asset of the latest release: a download, not the release page.
const ENGINE_ZIP_URL = RELEASES_URL + "/download/downloadmanagersquared-windows.zip";

// The guide, in the same repository (docs/guide.md); a section is added as #heading.
const GUIDE_URL = RELEASES_URL.replace(/\/releases\/latest$/, "/blob/main/docs/guide.md");

// Engine connection badge in the page header.
function renderConn(m) {
  const conn = $("conn");
  conn.textContent = m.connected ? "Engine connected" : m.notInstalled ? "Engine not installed" : m.quit ? "Engine quit" : "Engine offline";
  conn.className = "badge st " + (m.connected ? "ok" : "bad");
  conn.title = m.error || conn.textContent; // in the popup the square alone shows; its words on hover
  conn.hidden = m.connected && document.body.classList.contains("engine-window"); // the window is the engine: only trouble is news
}

// In the Downloads window both pages share one header: the engine badge
// (only when there is trouble), then tabs for Downloads and Settings. The tabs
// are in each page's HTML and early.js marks the window before anything is
// drawn, so nothing in the header moves as a page loads. Returns whether the
// page is in the window.
function windowHeader() {
  return document.body.classList.contains("engine-window");
}

// ifaceHue and IFACE_COLORS (the connection's colour) are in lib/iface.js.

// fmtBytes, fmtSpeed, fmtETA and percent are in lib/list.js.

function el(tag, attrs, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (k === "class") e.className = v;
    else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
    else if (v !== false && v !== null && v !== undefined) e.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) if (c !== null && c !== undefined && c !== false) e.append(c);
  return e;
}

// Whether torrent peers can connect in through an interface.
const INCOMING_TITLE = {
  reachable: "Torrent peers can connect in through this connection",
  forwarded: "The port you set for this VPN tunnel (not checked)",
  outgoing: "The router didn't open a port (no UPnP or NAT-PMP): torrents only use peers they connect to",
};

const VPN_MODES = [["off", "Off"], ["alongside", "VPN and direct"], ["vpn", "VPN only"]];
const VPN_TEXT = { alongside: "VPN and direct", vpn: "VPN only" };
const TUNNEL_TEXT = { up: "VPN up", connecting: "VPN connecting", down: "VPN retrying", off: "VPN off" };
const TUNNEL_CLASS = { up: "ok", connecting: "warn", down: "bad", off: "dim" };

function plural(n, word) {
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}

function ifaceName(id, state, settings) {
  const v = ((state && state.interfaces) || []).find((i) => i.id === id);
  if (v) return v.name;
  const s = settings && settings.interfaces && settings.interfaces[id];
  return (s && (s.label || s.name)) || id.slice(0, 8);
}

const $ = (id) => document.getElementById(id);

// Where a page was left, kept in this browser (or this window) only, for its next
// opening. Storage can be missing or refuse (a private window): then nothing is kept.
function recall(key) {
  try { return JSON.parse(localStorage.getItem("dms." + key)); } catch { return null; }
}
function remember(key, value) {
  try { localStorage.setItem("dms." + key, JSON.stringify(value)); } catch { /* nothing kept */ }
}

// Short message at the bottom of the page (needs an element with id "toast").
// It stays long enough to read: longer for longer text, up to 15 seconds.
// An error (toast(text, true)) looks different and stays until closed.
function toast(text, error) {
  const t = $("toast");
  clearTimeout(toast.timer);
  t.classList.toggle("error", !!error);
  t.setAttribute("role", error ? "alert" : "status");
  t.replaceChildren(text);
  if (error) {
    t.append(el("button", { class: "icon", title: "Close", "aria-label": "Close this message", onclick: () => t.classList.remove("show") }, "×"));
  }
  t.classList.add("show");
  if (!error) toast.timer = setTimeout(() => t.classList.remove("show"), Math.min(15000, Math.max(2500, text.length * 60)));
}

// Removes many downloads in one call. An engine without removeMany (older than
// this page) is asked per id, as before; a failure is a toast.
async function removeMany(ids, deleteFile) {
  if (!ids.length) return;
  try {
    await call("removeMany", { ids, deleteFile });
  } catch (e) {
    if (!/unknown method/.test(e.message)) return toast(e.message, true);
    await Promise.all(ids.map((id) => call("remove", { id, deleteFile }).catch((err) => toast(err.message, true))));
  }
}

// Calls the engine, reporting success or the error as a toast.
async function act(method, params, okText) {
  try {
    await call(method, params);
    if (okText) toast(okText);
  } catch (e) {
    toast(e.message, true);
  }
}
