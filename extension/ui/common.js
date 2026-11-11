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
  return Host.subscribe((m) => { applyAccent(m.settings); onUpdate(m); });
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

// A link that leaves DMS²: Host opens it (a new tab, or the default browser).
function link(url, text) {
  return el("a", { href: url, onclick: (e) => { e.preventDefault(); Host.openUrl(url); } }, text);
}

// Where the engine and the extension are released (build.ps1 -Repo sets the repo).
const RELEASES_URL = "https://github.com/unicrncndms/downloadmanagersquared/releases/latest";

// Engine connection badge in the page header.
function renderConn(m) {
  const conn = $("conn");
  conn.textContent = m.connected ? "Engine connected" : m.notInstalled ? "Engine not installed" : m.quit ? "Engine quit" : "Engine offline";
  conn.className = "badge st " + (m.connected ? "ok" : "bad");
  conn.title = m.error || "";
}

// In the Downloads window both pages share one header: the logo, the engine
// badge, then tabs for Downloads, Connections and Settings. The settings page
// has its own two (settings.js), and gets Downloads put in front. Returns
// whether the page is in the window.
function windowHeader() {
  if (!Host.openDownloads) return false;
  document.body.classList.add("windowed", "engine-window");
  const tab = (label, go) => el("button", { class: "tab", onclick: go }, label);
  const downloads = tab("Downloads", () => Host.openDownloads());
  if ($("pages")) {
    $("pages").prepend(downloads);
    return true;
  }
  downloads.setAttribute("aria-current", "page");
  downloads.onclick = null;
  $("conn").after(el("nav", { class: "tabs", "aria-label": "Pages" }, downloads,
    tab("Connections", () => Host.openSettings("#connections")),
    tab("Settings", () => Host.openSettings("#downloads"))));
  return true;
}

// ifaceHue is a connection's colour on the pages (ui.css --c0 …): its place
// among the engine's connections, so it is the same on every page.
function ifaceHue(id, state) {
  const i = ((state && state.interfaces) || []).findIndex((x) => x.id === id);
  return `var(--c${Math.max(i, 0) % 6})`;
}

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

// The progress line: the numbers, then the engine's note (or the note alone
// while the size is unknown).
function progressText(d) {
  const nums = d.size > 0 ? `${fmtBytes(d.done)} of ${fmtBytes(d.size)} (${percent(d).toFixed(1)}%)`
    : d.done > 0 ? fmtBytes(d.done) : "";
  return [nums, d.note].filter(Boolean).join(" · ");
}

// Whether torrent peers can connect in through an interface.
const INCOMING_TEXT = { reachable: "reachable", forwarded: "port forwarded", outgoing: "outgoing only" };
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
  return (s && s.name) || id.slice(0, 8);
}

const $ = (id) => document.getElementById(id);

// Short message at the bottom of the page (needs an element with id "toast").
// It stays long enough to read: longer for longer text, up to 15 seconds.
function toast(text) {
  const t = $("toast");
  t.textContent = text;
  t.classList.add("show");
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => t.classList.remove("show"), Math.min(15000, Math.max(2500, text.length * 60)));
}

// Calls the engine, reporting success or the error as a toast.
async function act(method, params, okText) {
  try {
    await call(method, params);
    if (okText) toast(okText);
  } catch (e) {
    toast(e.message);
  }
}
