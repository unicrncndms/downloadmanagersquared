"use strict";

// Shared by the downloads popup and the settings page.

function call(method, params) {
  return browser.runtime.sendMessage({ type: "call", method, params });
}

function connectUI(onUpdate) {
  const port = browser.runtime.connect({ name: "ui" });
  port.onMessage.addListener(onUpdate);
  return port;
}

// Where the engine and the extension are released (build.ps1 -Repo sets the repo).
const RELEASES_URL = "https://github.com/unicrncndms/downloadmanagersquared/releases/latest";

// Engine connection badge in the page header.
function renderConn(m, connectedText) {
  const conn = $("conn");
  conn.textContent = m.connected ? connectedText : m.notInstalled ? "Engine not installed" : "Engine offline";
  conn.className = "badge " + (m.connected ? "ok" : "bad");
  conn.title = m.error || "";
}

function fmtBytes(n) {
  if (!(n >= 0)) return "?";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1000 && i < u.length - 1) { n /= 1000; i++; }
  return `${n.toFixed(i && n < 100 ? 1 : 0)} ${u[i]}`;
}

function fmtSpeed(bps) {
  return bps >= 1 ? fmtBytes(bps) + "/s" : "0 B/s";
}

function fmtETA(sec) {
  if (!(sec >= 0) || !isFinite(sec)) return "—";
  const h = Math.floor(sec / 3600), m = Math.floor(sec / 60) % 60, s = Math.floor(sec % 60);
  if (h) return `${h}h ${String(m).padStart(2, "0")}m`;
  if (m) return `${m}m ${String(s).padStart(2, "0")}s`;
  return `${s}s`;
}

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
const INCOMING_TEXT = { reachable: "reachable", outgoing: "outgoing only" };
const INCOMING_TITLE = {
  reachable: "Torrent peers can connect in through this connection",
  outgoing: "The router didn't open a port (no UPnP or NAT-PMP): torrents only use peers they connect to",
};

const VPN_MODES = [["off", "Off"], ["alongside", "VPN and direct"], ["vpn", "VPN only"]];
const VPN_TEXT = { alongside: "VPN and direct", vpn: "VPN only" };
const TUNNEL_TEXT = { up: "VPN up", connecting: "VPN connecting", down: "VPN retrying", off: "VPN off" };
const TUNNEL_CLASS = { up: "ok", connecting: "warn", down: "bad", off: "dim" };

function plural(n, word) {
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}

function percent(d) {
  return d.size > 0 ? Math.min(100, (d.done / d.size) * 100) : 0;
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
