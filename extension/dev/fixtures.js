"use strict";

// States the fake background feeds the UI pages, chosen with ?fixture=<name>.
// window.feed(FIXTURES.x) switches state on an open page.

const IFACES = [
  { id: "eth", name: "Ethernet", description: "Intel I225-V", ip: "192.168.1.20", present: true, enabled: true, isDefault: true, isMain: true,
    connections: 4, socksPort: 50001, speed: 7.1e6, upload: true, uploadLimit: 0, downloadLimit: 0, incoming: "reachable" },
  { id: "wifi", name: "Wi-Fi", description: "Intel AX211", ip: "192.168.2.33", present: true, enabled: true,
    connections: 4, socksPort: 50002, speed: 4.4e6, upload: true, uploadLimit: 0, downloadLimit: 0, incoming: "outgoing" },
  { id: "corp", name: "Corporate VPN", description: "WireGuard Tunnel", ip: "10.8.0.2", present: true, enabled: false, vpn: true,
    connections: 4, socksPort: 50003, speed: 0, upload: true, uploadLimit: 0, downloadLimit: 0 },
  { id: "eth~vpn", name: "VPN via Ethernet", ip: "10.2.0.2/32", present: true, enabled: true, tunnel: true, of: "eth", provider: "conf",
    tunnelState: "up", connections: 4, socksPort: 50004, speed: 2.1e6, upload: true, uploadLimit: 0, downloadLimit: 0,
    incoming: "forwarded", forwardedPort: 41000 },
];

const SETTINGS = {
  interfaces: { eth: { name: "Ethernet" }, wifi: { name: "Wi-Fi" }, corp: { name: "Corporate VPN" } },
  mainInterface: "", thresholdBytes: 100e6, saveDir: "C:\\Users\\you\\Downloads", maxParallel: 1,
  etaWindowSec: 60, defaultConnections: 4, seedRatio: 1, torrentPort: 41234, vpnHttp: "off", vpnTorrent: "off", piaRegion: "", vpnProvider: "",
};

const DOWNLOADS = [
  { kind: "http", id: "a", url: "https://files.example.com/dl/abc", pageUrl: "https://example.com/file/abc", filename: "ubuntu-24.04.iso",
    dir: "C:\\Users\\you\\Downloads", size: 6.1e9, done: 2.4e9, status: "running", speed: 11.5e6, eta: 322, probed: true, rangeOk: true,
    label: "Downloading", actions: { pause: true, showInFolder: true, remove: true }, partialFile: true,
    links: { eth: { status: "ready", conns: 4, speed: 7.1e6, resolved: false, label: "Downloading", tone: "ok" },
      wifi: { status: "ready", conns: 2, speed: 4.4e6, resolved: true, label: "Server busy · 2 of 4 connections", tone: "warn" } },
    added: "2026-11-03T20:00:00Z" },
  { kind: "http", id: "b", url: "https://cdn.example.org/big.zip", filename: "big.zip", dir: "C:\\Users\\you\\Downloads",
    size: 3e9, done: 3e9, status: "completed", speed: 0, eta: -1, probed: true, rangeOk: true, links: {},
    label: "Completed", tone: "ok", actions: { showInFolder: true, remove: true }, partialFile: false, added: "2026-11-03T19:00:00Z" },
  { kind: "torrent", id: "c", url: "magnet:?xt=urn:btih:2da0", filename: "debian-13.iso", dir: "C:\\Users\\you\\Downloads", size: 7.9e8, done: 7.9e8,
    status: "seeding", speed: 0, upSpeed: 3e5, eta: -1, uploaded: 1.2e8, ratio: 0.15, peers: 6, seeds: 0, swarm: 40, finished: "2026-11-03T18:00:00Z",
    inSwarm: true, label: "Seeding", tone: "ok", note: "complete", actions: { pause: true, showInFolder: true, remove: true }, partialFile: false,
    links: { eth: { status: "ready", conns: 3, label: "Seeding", tone: "ok" }, wifi: { status: "ready", conns: 2, label: "Seeding", tone: "ok" } }, added: "2026-11-03T18:00:00Z" },
];

const STATE = { downloads: DOWNLOADS, interfaces: IFACES, pia: { state: "off" }, speed: 11.5e6, eta: 322, active: 1 };

const RECIPES = {
  "example.com": { steps: [{ tag: "button", text: "Free download" }, { tag: "a", text: "Start download" }], learned: Date.parse("2026-10-20") },
};

const FIXTURES = {
  downloads: { connected: true, state: STATE, settings: SETTINGS, autostart: true, recipes: RECIPES, engineVersion: "0.3.0" },
  empty: { connected: true, state: { ...STATE, downloads: [], active: 0, speed: 0 }, settings: SETTINGS, autostart: true, recipes: {}, engineVersion: "0.3.0" },
  offline: { connected: false, error: "engine disconnected", state: null, settings: null, recipes: RECIPES },
  notInstalled: { connected: false, notInstalled: true, error: "No such native application downloadmanagersquared", state: null, settings: null, recipes: {} },
  otherVersion: { connected: true, state: STATE, settings: SETTINGS, autostart: true, recipes: RECIPES, engineVersion: "0.4.1" },
  // The Downloads window: no recipes or user agent here, and the versions always match.
  window: { connected: true, state: STATE, settings: { ...SETTINGS, vpnProvider: "pia" }, autostart: true, recipes: null, engineVersion: "0.3.0",
    host: { version: "0.3.0", userAgent: null, recipesHere: false, openDownloads: true } },
};

window.fixture = () => FIXTURES[new URLSearchParams(location.search).get("fixture") || "downloads"] || FIXTURES.downloads;
