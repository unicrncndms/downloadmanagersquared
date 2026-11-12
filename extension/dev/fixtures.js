"use strict";

// States the fake background feeds the UI pages, chosen with ?fixture=<name>.
// window.feed(FIXTURES.x) switches state on an open page.

const IFACES = [
  { id: "eth", color: 0, name: "Ethernet", description: "Intel I225-V", ip: "192.168.1.20", present: true, enabled: true, isDefault: true, isMain: true,
    connections: 4, socksPort: 50001, speed: 7.1e6, upload: true, uploadLimit: 0, downloadLimit: 0, incoming: "reachable" },
  { id: "wifi", color: 1, name: "Wi-Fi", description: "Intel AX211", ip: "192.168.2.33", present: true, enabled: true,
    connections: 4, socksPort: 50002, speed: 4.4e6, upload: true, uploadLimit: 0, downloadLimit: 0, incoming: "outgoing" },
  { id: "corp", color: 2, name: "Corporate VPN", description: "WireGuard Tunnel", ip: "10.8.0.2", present: true, enabled: false, vpn: true,
    connections: 4, socksPort: 50003, speed: 0, upload: true, uploadLimit: 0, downloadLimit: 0 },
  { id: "eth~vpn", color: 0, name: "VPN via Ethernet", ip: "10.2.0.2/32", present: true, enabled: true, tunnel: true, of: "eth", provider: "conf",
    tunnelState: "up", connections: 4, socksPort: 50004, speed: 2.1e6, upload: true, uploadLimit: 0, downloadLimit: 0,
    incoming: "forwarded", forwardedPort: 41000 },
];

const SETTINGS = {
  interfaces: { eth: { name: "Ethernet" }, wifi: { name: "Wi-Fi" }, corp: { name: "Corporate VPN" } },
  mainInterface: "", thresholdBytes: 100e6, saveDir: "C:\\Users\\you\\Downloads", maxParallel: 1,
  etaWindowSec: 60, defaultConnections: 4, seedRatio: 1, torrentPort: 41234, vpnHttp: "off", vpnTorrent: "off", piaRegion: "", vpnProvider: "",
};

// A file's map as the engine sends it: runs of [on disk, being fetched by, missing].
// who: the letter of the connection that wrote the run on disk ("#": not known).
const chunkMap = (...runs) => runs.map(([on, by, off, who = "#"]) => who.repeat(on) + by + ".".repeat(off)).join("");

const DOWNLOADS = [
  { map: chunkMap([9, 0, 6, "a"], [7, 0, 8, "a"], [8, 1, 7, "b"], [5, 0, 10, "a"], [6, 1, 9, "b"], [2, 0, 13, "a"]), mapBy: ["eth", "wifi"], kind: "http", id: "a", url: "https://files.example.com/dl/abc", pageUrl: "https://example.com/file/abc", filename: "ubuntu-24.04.iso",
    dir: "C:\\Users\\you\\Downloads", size: 6.1e9, done: 2.4e9, status: "running", speed: 11.5e6, eta: 322, probed: true, rangeOk: true,
    label: "Downloading", actions: { pause: true, showInFolder: true, remove: true }, partialFile: true,
    links: { eth: { status: "ready", conns: 4, speed: 7.1e6, resolved: false, label: "Downloading", tone: "ok" },
      wifi: { status: "ready", conns: 2, speed: 4.4e6, resolved: true, label: "Server busy · 2 of 4 connections", tone: "warn" } },
    added: "2026-11-03T20:00:00Z" },
  // One interface, a site with no recipe yet: the Record recipe button.
  { map: chunkMap([4, "", 0, "#"], [7, "0000", 81, "a"]), mapBy: ["eth"], kind: "http", id: "r", url: "https://dl.filehost.example/k/77", pageUrl: "https://filehost.example/f/77", filename: "dataset-2026.tar",
    dir: "C:\\Users\\you\\Downloads", size: 1.8e10, done: 2.1e9, status: "running", speed: 5.3e6, eta: 3000, probed: true, rangeOk: true,
    label: "Downloading", actions: { pause: true, showInFolder: true, remove: true, recordRecipe: true }, partialFile: true,
    links: { eth: { status: "ready", conns: 4, speed: 5.3e6, resolved: false, label: "Downloading", tone: "ok" } },
    added: "2026-11-03T20:30:00Z" },
  { kind: "http", id: "b", url: "https://cdn.example.org/big.zip", filename: "big.zip", dir: "C:\\Users\\you\\Downloads",
    size: 3e9, done: 3e9, status: "completed", speed: 0, eta: -1, probed: true, rangeOk: true, links: {},
    label: "Completed", tone: "ok", actions: { showInFolder: true, remove: true }, partialFile: false, added: "2026-11-03T19:00:00Z" },
  { kind: "torrent", id: "c", url: "magnet:?xt=urn:btih:2da0", filename: "debian-13.iso", dir: "C:\\Users\\you\\Downloads", size: 7.9e8, done: 7.9e8,
    status: "seeding", speed: 0, upSpeed: 3e5, eta: -1, uploaded: 1.2e8, ratio: 0.15, peers: 6, seeds: 0, swarm: 40, finished: "2026-11-03T18:00:00Z",
    inSwarm: true, label: "Seeding", tone: "ok", note: "complete", actions: { pause: true, showInFolder: true, remove: true }, partialFile: false,
    links: { eth: { status: "ready", conns: 3, label: "Seeding", tone: "ok" }, wifi: { status: "ready", conns: 2, label: "Seeding", tone: "ok" } }, added: "2026-11-03T18:00:00Z" },
];

// Download as folder: waiting for a folder, extracting (locked), and unavailable.
const zipDownload = (id, filename, folder, extra) => ({
  map: chunkMap([29, "0000", 63, "a"]), mapBy: ["eth"], kind: "http", id, url: `https://cdn.example.org/${filename}`, filename, dir: "C:\\Users\\you\\Downloads", size: 4.2e10, done: 1.3e10,
  status: "running", speed: 9.2e6, eta: 3150, probed: true, rangeOk: true, label: "Downloading",
  actions: { pause: true, showInFolder: true, remove: true }, partialFile: true,
  links: { eth: { status: "ready", conns: 4, speed: 9.2e6, label: "Downloading", tone: "ok" } }, added: "2026-11-06T10:00:00Z", folder, ...extra,
});
const FOLDER_DOWNLOADS = [
  zipDownload("f3", "photos.zip", { on: false, files: 0, of: 0, why: "A checksum is entered, and only a whole file can be checked against it: clear it first" },
    { sha256: "a".repeat(64) }),
  zipDownload("f2", "dataset.zip", { on: true, into: "E:\\Data", out: "E:\\Data\\dataset", locked: true, why: "Some files are already extracted and their space freed, so this can't change any more", keep: "auto", keeping: "free", keepFixed: true, files: 412, of: 1280,
    extracted: 9e9, phase: "Waiting for the download", extractEta: 3150,
    refetch: 3435973837, moveWarning: "Moving now re-downloads about 3.2 GiB",
    renamed: ["CON.txt → _CON.txt", "notes:v2.md → notes_v2.md"], note: "The server's checksum wasn't checked: the archive was extracted as it downloaded, so there was no whole file to check" },
    { note: "412 of 1,280 files extracted" }),
  zipDownload("f1", "big.zip", { on: true, files: 0, of: 0 }, { note: "choose a folder to extract into" }),
];

// A Metalink of two files: one group card, each file on its own mirrors.
const metaFile = (id, filename, extra) => ({
  kind: "http", id, url: `https://mirror-a.example.org/${filename}`, filename, dir: "C:\\Users\\you\\Downloads", size: 4.7e9, done: 1.2e9,
  status: "running", speed: 8.4e6, eta: 420, probed: true, rangeOk: true, label: "Downloading", sha256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  actions: { pause: true, showInFolder: true, remove: true }, partialFile: true, group: "m", groupName: "fedora-43",
  mirrors: [{ url: `https://mirror-a.example.org/${filename}` }, { url: `https://ftp.uni.example/pub/${filename}` },
    { url: `https://bad.example.net/${filename}`, dropped: "different file size (4700000001 vs 4700000000 bytes)" }],
  links: { eth: { status: "ready", conns: 4, speed: 5.1e6, label: "Downloading", tone: "ok", mirror: "mirror-a.example.org" },
    wifi: { status: "ready", conns: 3, speed: 3.3e6, label: "Downloading", tone: "ok", mirror: "ftp.uni.example" } },
  added: "2026-11-07T09:00:00Z", ...extra,
});
const METALINK_DOWNLOADS = [
  metaFile("m1", "Fedora-43-x86_64.iso"),
  metaFile("m2", "Fedora-43-aarch64.iso", { status: "queued", done: 0, speed: 0, eta: -1, label: "Queued", tone: "dim", actions: { resume: false, pause: true, remove: true }, partialFile: false }),
  DOWNLOADS[0],
];

// A RAR split into parts: the engine groups them by name and folder.
const part = (n, extra) => metaFile("p" + n, `holiday-video.part${n}.rar`, { mirrors: undefined, sha256: "", size: 1e9,
  group: "parts:holiday-video.rar", groupName: "holiday-video.rar", folder: { on: false, files: 0, of: 0 }, ...extra });
const SPLIT_DOWNLOADS = [
  part(1, { status: "completed", done: 1e9, speed: 0, eta: -1, label: "Completed", tone: "ok", partialFile: false }),
  part(2, { done: 4e8 }),
  part(3, { status: "queued", size: -1, done: 0, speed: 0, eta: -1, label: "Queued", tone: "dim", partialFile: false }),
  DOWNLOADS[0],
];

const STATE = { downloads: DOWNLOADS, interfaces: IFACES, pia: { state: "off" }, speed: 11.5e6, eta: 322, active: 1 };

const RECIPES = {
  "example.com": { steps: [{ tag: "button", text: "Free download" }, { tag: "a", text: "Start download" }], learned: Date.parse("2026-10-20") },
};

// A torrent waiting for its files to be chosen: the state has only their count.
const CHOOSING = { kind: "torrent", id: "t", url: "magnet:?xt=urn:btih:9f1c", filename: "datasets", dir: "C:\\Users\\you\\Downloads",
  size: 3e9, done: 0, status: "choosing", speed: 0, eta: -1, fileCount: 2, filesSize: 3e9, label: "Choose files",
  actions: { remove: true }, partialFile: false, links: {}, added: "2026-11-06T11:00:00Z" };

// Hardening: long, emoji, CJK and RTL names, huge sizes, six connections with
// long names, a failed link, and 300 downloads in the queue.
const LONG = "Very.Long.Release.Name.With.Many.Dots.And.No.Spaces.2026.Remastered.Director's.Cut.Extended.Edition.4K.HDR.x265.mkv";
const STRESS_IFACES = [
  ...IFACES,
  { id: "phone", color: 3, name: "Pixel 9 Pro USB-Tethering (Remote NDIS based Internet Sharing Device #2)", description: "Remote NDIS", ip: "192.168.42.129",
    present: true, enabled: true, connections: 32, socksPort: 50005, speed: 1.23e9, upload: true, uploadLimit: 0, downloadLimit: 0, incoming: "outgoing" },
  { id: "lte", color: 4, name: "Mobilfunk-Breitbandverbindung", description: "Qualcomm X65", ip: "10.64.1.9",
    present: true, enabled: true, connections: 4, socksPort: 50006, speed: 0, upload: true, uploadLimit: 0, downloadLimit: 0 },
];
const STRESS = { connected: true, settings: { ...SETTINGS, saveDir: "D:\\" + "Sehr langer Ordnername\\".repeat(6) }, autostart: true, recipes: RECIPES, engineVersion: "0.3.0",
  state: { ...STATE, interfaces: STRESS_IFACES, speed: 1.24e9, eta: 987654, active: 3, downloads: [
    { ...DOWNLOADS[0], id: "s1", filename: LONG, size: 9.87e12, done: 1.2e12, speed: 1.24e9, eta: 987654,
      links: { eth: DOWNLOADS[0].links.eth, wifi: DOWNLOADS[0].links.wifi,
        phone: { status: "ready", conns: 32, speed: 1.23e9, label: "Downloading", tone: "ok" },
        lte: { status: "failed", conns: 0, speed: 0, label: "Failed: the server answered 403 Forbidden for this connection's own link", tone: "bad" },
        "eth~vpn": { status: "ready", conns: 4, speed: 2.1e6, label: "Downloading", tone: "ok" } }, mapBy: ["eth", "wifi", "phone"] },
    { ...DOWNLOADS[0], id: "s2", filename: "🎬 映画コレクション 2026 — 完全版.zip", links: { eth: DOWNLOADS[0].links.eth } },
    { ...DOWNLOADS[0], id: "s3", filename: "نسخة احتياطية كاملة للمشروع.7z", links: { wifi: DOWNLOADS[0].links.wifi } },
    { ...DOWNLOADS[1], id: "s4", status: "failed", speed: 0, eta: -1, label: "Failed", tone: "bad",
      note: "The server refused every connection: 404 Not Found. The link may have expired; open the page again to get a new one.", actions: { remove: true, retry: true } },
    ...Array.from({ length: 300 }, (_, i) => ({ ...DOWNLOADS[0], id: "q" + i, filename: `episode-${String(i + 1).padStart(3, "0")}.mkv`,
      url: `https://example.com/e/${i}`, status: "queued", done: 0, map: "", note: "Waiting in the queue", speed: 0, eta: -1, label: "Queued", tone: "dim",
      links: {}, partialFile: false, actions: { remove: true, pause: true } })),
    { ...DOWNLOADS[2], id: "s5", filename: "x", size: 1 },
  ] } };

const FIXTURES = {
  choosing: { connected: true, state: { ...STATE, downloads: [CHOOSING], active: 0 }, settings: SETTINGS, autostart: true, recipes: RECIPES,
    engineVersion: "0.3.0", files: { t: [{ path: "datasets/a.csv", size: 1e9, selected: true }, { path: "datasets/b.csv", size: 2e9, selected: false }] } },
  downloads: { connected: true, state: STATE, settings: SETTINGS, autostart: true, recipes: RECIPES, engineVersion: "0.3.0",
    logs: { a: [
      { time: "2026-11-03T20:00:01Z", text: "Queued" },
      { time: "2026-11-03T20:00:02Z", text: "Downloading" },
      { time: "2026-11-03T20:00:03Z", iface: "eth", text: "Downloading" },
      { time: "2026-11-03T20:00:04Z", iface: "wifi", text: "Getting its own link" },
      { time: "2026-11-03T20:00:20Z", iface: "wifi", text: "Server busy · 2 of 4 connections" },
    ] } },
  folder: { connected: true, state: { ...STATE, downloads: FOLDER_DOWNLOADS, active: 3 }, settings: { ...SETTINGS, folderDir: "E:\\Data", folderDirs: ["E:\\Data", "D:\\Games\\Mods", "F:\\Archive"] },
    autostart: true, recipes: RECIPES, engineVersion: "0.3.0" },
  // Ethernet and Wi-Fi behind one router; Check lines finds them on one public IP.
  uplink: { connected: true, settings: SETTINGS, autostart: true, recipes: RECIPES, engineVersion: "0.3.0",
    state: { ...STATE, interfaces: IFACES.map((i) => (i.id === "eth" ? { ...i, sameRouter: ["Wi-Fi"] } : i.id === "wifi" ? { ...i, sameRouter: ["Ethernet"] } : i)) },
    lines: [{ id: "eth", color: 0, name: "Ethernet", ip: "203.0.113.7" }, { id: "wifi", color: 1, name: "Wi-Fi", ip: "203.0.113.7" }] },
  // IPv6 on: Wi-Fi is dual-stack, a phone tether has only IPv6.
  ipv6: { connected: true, settings: { ...SETTINGS, ipv6: true }, autostart: true, recipes: RECIPES, engineVersion: "0.3.0",
    state: { ...STATE, downloads: [{ ...DOWNLOADS[0], links: { ...DOWNLOADS[0].links,
      wifi: { ...DOWNLOADS[0].links.wifi, family: "IPv6" },
      phone: { status: "ready", conns: 4, speed: 1.3e6, family: "IPv6", label: "Downloading", tone: "ok" } } }, ...DOWNLOADS.slice(1)], interfaces: [
      ...IFACES.map((i) => (i.id === "wifi" ? { ...i, ip6: "2a02:2c60:f110:a80:1e29:3239:c102:ce4a", families: ["IPv4", "IPv6"] } : i.tunnel ? i : { ...i, families: ["IPv4"] })),
      { id: "phone", color: 3, name: "Phone", description: "Remote NDIS", ip: "2a00:20:6000::42", ip6: "2a00:20:6000::42", families: ["IPv6"], present: true, enabled: true,
        connections: 4, socksPort: 50005, speed: 1.3e6, upload: true, uploadLimit: 0, downloadLimit: 0, incoming: "outgoing" }] } },
  // IPv6 off, with an adapter that has only IPv6: the hint.
  ipv6hint: { connected: true, settings: SETTINGS, autostart: true, recipes: RECIPES, engineVersion: "0.3.0", state: { ...STATE, ipv6Only: ["Phone"] } },
  // Three queued behind one downloading: the up and down arrows, and dragging.
  queue: { connected: true, settings: SETTINGS, autostart: true, recipes: RECIPES, engineVersion: "0.3.0",
    state: { ...STATE, downloads: [DOWNLOADS[0], ...["one.iso", "two.iso", "three.iso"].map((n) => ({ ...DOWNLOADS[0], id: n, filename: n, url: "https://example.com/" + n,
      status: "queued", done: 0, map: "", note: "Waiting in the queue", speed: 0, eta: -1, label: "Queued", tone: "dim", links: {}, partialFile: false, actions: { remove: true, pause: true } }))] } },
  // An encrypted split archive, every part fetched: the group card asks for the password.
  splitPassword: { connected: true, settings: SETTINGS, autostart: true, recipes: RECIPES, engineVersion: "0.3.0",
    state: { ...STATE, downloads: [1, 2].map((n) => part(n, { status: "completed", done: 1e9, speed: 0, eta: -1, label: "Completed", tone: "ok", partialFile: false,
      folder: { on: true, into: "E:\\Data", files: 0, of: 0, ask: n === 1 ? "password" : "", set: { parts: 2, note: "Needs the archive's password", files: 0, of: 0 } } })) } },
  split: { connected: true, state: { ...STATE, downloads: SPLIT_DOWNLOADS }, settings: SETTINGS, autostart: true, recipes: RECIPES, engineVersion: "0.3.0" },
  metalink: { connected: true,
 state: { ...STATE, downloads: METALINK_DOWNLOADS }, settings: SETTINGS, autostart: true, recipes: RECIPES, engineVersion: "0.3.0" },
  // A split 7z extracting as one folder, part 2 not added.
  splitGap: { connected: true, state: { ...STATE, downloads: [1, 3].map((n) => metaFile("s" + n, `holiday-video.7z.00${n}`, {
    mirrors: undefined, sha256: "", size: n === 3 ? 3e8 : 1e9, done: n === 3 ? 3e8 : 1e9, status: "completed", speed: 0, eta: -1,
    label: "Completed", tone: "ok", partialFile: false, note: "Part 2 missing", group: "parts:holiday-video.7z", groupName: "holiday-video.7z",
    folder: { on: true, into: "E:\\Data", files: 0, of: 0, set: { parts: 3, missing: [2], note: "Part 2 missing", files: 12, of: 40, extracted: 9e8, total: 2.3e9 } } })) },
    settings: SETTINGS, autostart: true, recipes: RECIPES, engineVersion: "0.3.0" },
  // The accent: a dark Firefox theme's color, a pale one, and a preset chosen over the theme's.
  themeDark: { connected: true, state: STATE, settings: { ...SETTINGS, themeAccent: "#0b3d2e" }, autostart: true, recipes: RECIPES, engineVersion: "0.3.0" },
  themeLight: { connected: true, state: STATE, settings: { ...SETTINGS, themeAccent: "#ffd84d" }, autostart: true, recipes: RECIPES, engineVersion: "0.3.0" },
  preset: { connected: true, state: STATE, settings: { ...SETTINGS, accent: "#db2777", themeAccent: "#0b3d2e" }, autostart: true, recipes: RECIPES, engineVersion: "0.3.0" },
  // An encrypted archive waiting for its password, and one whose password was wrong.
  password: { connected: true, state: { ...STATE, downloads: [
    zipDownload("p1", "backup.7z", { on: true, into: "E:\\Data", ask: "password", files: 0, of: 0 }, { note: "needs the archive's password" }),
    zipDownload("p2", "photos.rar", { on: true, into: "E:\\Data", ask: "wrong", files: 0, of: 0 }, { note: "needs the archive's password" }),
  ] }, settings: SETTINGS, autostart: true, recipes: RECIPES, engineVersion: "0.3.0" },
  empty: { connected: true, state: { ...STATE, downloads: [], active: 0, speed: 0 }, settings: SETTINGS, autostart: true, recipes: {}, engineVersion: "0.3.0" },
  offline: { connected: false, error: "engine disconnected", state: null, settings: null, recipes: RECIPES },
  quit: { connected: false, quit: true, error: "", state: null, settings: null, recipes: RECIPES },
  notInstalled: { connected: false, notInstalled: true, error: "No such native application downloadmanagersquared", state: null, settings: null, recipes: {} },
  otherVersion: { connected: true, state: STATE, settings: SETTINGS, autostart: true, recipes: RECIPES, engineVersion: "0.4.1" },
  stress: STRESS,
  // The Downloads window: no recipes or user agent here, and the versions always match.
  window: { connected: true, state: STATE, settings: { ...SETTINGS, vpnProvider: "pia" }, autostart: true, recipes: null, engineVersion: "0.3.0",
    host: { version: "0.3.0", userAgent: null, recipesHere: false, openDownloads: true } },
};

// What the real engine adds to every download and to each group of them (Present
// and Groups in engine/internal/download): the section, whether the label is plain, and the group summaries.
// The fake state is written by hand, so this fills them in from its statuses.
const SECTION_OF = { running: "active", verifying: "active", metadata: "active", checking: "active", seeding: "seeding", queued: "queued", paused: "paused", completed: "done" };
const RANKS = ["needs", "active", "seeding", "queued", "paused", "done"];
function sectionOf(d) {
  const f = d.kind !== "torrent" && d.folder && d.folder.on ? d.folder : null;
  const set = d.folder && d.folder.set;
  const fetched = d.status === "completed" && !(set && !set.finished);
  const asking = f && !f.into && d.status !== "completed";
  const stuck = f && !fetched && (f.ask || (set && set.missing && set.missing.length));
  if (d.status === "failed" || d.status === "choosing" || asking || stuck || Object.values(d.links || {}).some((l) => l.canRetry) || (d.status === "paused" && d.error && !d.waitVpn)) return "needs";
  return d.status === "completed" && !fetched ? "active" : SECTION_OF[d.status] || "active";
}
// A label that only says what the status does, as Present's Plain.
const PLAIN = { running: "Downloading", seeding: "Seeding", paused: "Paused", queued: "Queued" };
function plainOf(d) {
  return PLAIN[d.status] === d.label && d.tone !== "warn" && d.tone !== "bad";
}
function groupsOf(downloads) {
  const by = new Map();
  for (const d of downloads) if (d.group) by.set(d.group, [...(by.get(d.group) || []), d]);
  return [...by].filter(([, items]) => items.length > 1).map(([id, all]) => {
    const rank = (d) => (d.status === "completed" ? 5 : RANKS.indexOf(d.section));
    const items = [...all].sort((a, b) => rank(a) - rank(b) || String(a.filename).localeCompare(String(b.filename), undefined, { numeric: true }));
    const set = items.map((d) => d.folder && d.folder.set).find(Boolean);
    const done = items.filter((d) => d.status === "completed").length;
    const waiting = set && !set.finished;
    const finished = done === items.length && !waiting;
    const size = items.every((d) => d.size > 0) ? items.reduce((a, d) => a + d.size, 0) : set && set.total > 0 ? set.total : -1;
    const [label, tone] = finished ? ["Completed", "ok"] : done === items.length ? (set.note ? ["Waiting", "warn"] : ["Extracting", ""]) : [items[0].label, items[0].tone || ""];
    const can = (k) => items.some((d) => d.actions && d.actions[k]);
    return { id, name: items[0].groupName || "Metalink", parts: id.startsWith("parts:"), count: set && set.parts ? set.parts : items.length, items: items.length, done, size,
      got: items.reduce((a, d) => a + (d.done || 0), 0), speed: items.reduce((a, d) => a + (d.status === "running" ? d.speed || 0 : 0), 0), finished, label, tone,
      pause: can("pause"), resume: can("resume"), partialFile: items.some((d) => d.partialFile), files: waiting ? set.files : 0, of: waiting ? set.of : 0, order: items.map((d) => d.id) };
  });
}
for (const f of Object.values(FIXTURES)) {
  if (!f.state || !f.state.downloads) continue;
  const downloads = f.state.downloads.map((d) => ({ ...d, section: sectionOf(d), plain: d.plain ?? plainOf(d) }));
  f.state = { ...f.state, downloads, groups: groupsOf(downloads) };
}

window.fixture = () => FIXTURES[new URLSearchParams(location.search).get("fixture") || "downloads"] || FIXTURES.downloads;
