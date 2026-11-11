"use strict";

// Two pages in one: Connections (with the VPN), and Settings, chosen by the
// address's #. Every change is saved as soon as it's made.

const GB = 1e9;
const MB = 1e6;

let ifaceSig = "";
let recipesSig = "";
let latestSettings = null;
let latestState = {};

windowHeader();

// --- connections ---

$("refreshIfaces").onclick = () => act("refreshInterfaces", {}, "Connections refreshed");
// Only on this click does DMS² contact a STUN server.
$("checkLines").onclick = async () => {
  const btn = $("checkLines");
  const box = $("lineResult");
  btn.disabled = true;
  box.hidden = false;
  box.replaceChildren(el("div", { class: "muted" }, "Asking through each connection…"));
  try {
    const r = lineResults(await call("checkLines", {}));
    box.replaceChildren(...r.lines.map((t) => el("div", { class: "muted" }, t)), ...r.warnings.map((t) => el("div", { class: "warn" }, t)));
    if (!r.lines.length) box.replaceChildren(el("div", { class: "muted" }, "No connection in use to check."));
  } catch (e) {
    box.replaceChildren(el("div", { class: "bad" }, e.message));
  } finally {
    btn.disabled = false;
  }
};
$("followWindows").addEventListener("change", () => {
  const main = (latestState.interfaces || []).find((i) => i.isMain);
  act("setSettings", { mainInterface: $("followWindows").checked ? "" : (main ? main.id : "") },
    $("followWindows").checked ? "Main connection follows Windows" : "Main connection fixed");
});

// Importing a .conf: the file picker is shared, this is the connection it's for.
let confFor = "";
$("confFile").addEventListener("change", async () => {
  const f = $("confFile").files[0];
  $("confFile").value = "";
  if (f) act("importTunnel", { iface: confFor, conf: await f.text() }, "VPN tunnel imported");
});

function badge(text, cls, title) {
  return el("span", { class: "badge " + (cls || "dim"), title }, text);
}

function vpnCell(i, tun) {
  if (!tun) {
    return el("div", { class: "vpn" },
      el("button", { class: "small", onclick: () => { confFor = i.id; $("confFile").click(); } }, "Import .conf"));
  }
  const pia = tun.provider === "pia";
  // The port the VPN provider forwards to this tunnel, where it can't tell DMS² itself.
  let port = null;
  if (!pia) {
    port = el("input", { type: "number", class: "port", min: 1, max: 65535, placeholder: "Port", "aria-label": `Forwarded port: ${i.name}`, title: "A port your VPN provider forwards to this connection, for torrent peers to connect in (empty: none, or found by NAT-PMP). AirVPN: leave the port's Local field empty, and with several connections give each its own port, linked to that connection's device.", value: tun.forwardedPort || "" });
    port.addEventListener("change", () => {
      const n = Number(port.value) || 0;
      act("setForwardedPort", { iface: i.id, port: n }, n ? `Forwarded port ${n} set` : "Forwarded port cleared");
    });
  }
  return el("div", { class: "vpn" },
    badge((pia ? "PIA · " : "") + (TUNNEL_TEXT[tun.tunnelState] || tun.tunnelState).replace(/^VPN /, ""),
      TUNNEL_CLASS[tun.tunnelState], tun.tunnelError || tun.ip),
    port,
    pia ? null : el("button", { class: "small", onclick: () => act("removeTunnel", { iface: i.id }, "VPN tunnel removed") }, "Remove"));
}

// A connection's state now, in a word: what the row leads with.
function connState(i, tun) {
  if (!i.present) return ["Not connected", "dim"];
  if (!i.enabled) return ["Off", "dim"];
  if (tun && tun.tunnelState === "down") return ["VPN retrying", "warn"];
  const speed = (i.speed || 0) + ((tun && tun.speed) || 0);
  return speed > 0 ? ["Downloading", "ok"] : ["Ready", ""];
}

const openConns = new Set(); // the connections whose settings are shown

function ifaceRow(i, tun, settings) {
  const set = (patch, ok) => act("setInterface", Object.assign({ id: i.id }, patch), ok);
  // Each control is named after its column, for screen readers.
  const check = (on, key, label) => {
    const c = el("input", { type: "checkbox", checked: on, "aria-label": `${label}: ${i.name}` });
    c.addEventListener("change", () => set({ [key]: c.checked }));
    return c;
  };
  const conns = el("input", { type: "number", min: 1, max: 32, value: i.connections, "aria-label": `Requests at once: ${i.name}` });
  conns.addEventListener("change", () => set({ connections: Number(conns.value) }));
  const limit = (bps, key, title) => {
    const input = el("input", { type: "number", min: 0, step: "any", placeholder: "–", title: title + " (empty: none)", "aria-label": `${title} in MB/s: ${i.name}`, value: bps ? +(bps / MB).toFixed(3) : "" });
    input.addEventListener("change", () => set({ [key]: Math.round((Number(input.value) || 0) * MB) }));
    return input;
  };
  // Shows the main connection in effect, chosen or Windows'.
  const main = el("input", { type: "radio", name: "main", checked: i.isMain, "aria-label": `Main connection: ${i.name}` });
  main.addEventListener("change", () => act("setSettings", { mainInterface: i.id }, `${i.name} is now the main connection`));

  // What the connection is, then the few facts worth a badge.
  const badges = [
    familyText(i) ? badge(familyText(i), "dim", i.ip6 && i.ip6 !== i.ip ? i.ip6 : undefined) : null,
    i.isDefault ? badge("Windows default") : null,
    i.vpn ? badge("System VPN", "warn") : null,
    i.incoming ? badge(i.incoming === "reachable" ? "Reachable" : "Outgoing only", i.incoming === "reachable" ? "ok" : "dim", INCOMING_TITLE[i.incoming]) : null,
  ].filter(Boolean);
  // First what it is and does now; its settings fold under it.
  const [word, tone] = connState(i, tun);
  const open = openConns.has(i.id);
  const more = el("div", { class: "conn-more", hidden: !open },
    el("label", {}, main, " Main connection",
      el("span", { class: "info", tabindex: "0", "data-tip": "Used alone for servers that can't split a file, and it decides which file the other connections must match." })),
    el("label", {}, "Requests at once ", conns,
      el("span", { class: "info", tabindex: "0", "data-tip": "How many parts of a download this connection fetches at the same time." })),
    el("label", {}, check(i.upload, "upload", "Upload"), " Upload"),
    el("span", { class: "limits" }, "Limit (MB/s) ↓", limit(i.downloadLimit, "downloadLimit", "Download limit"), "↑", limit(i.uploadLimit, "uploadLimit", "Upload limit")),
    el("span", { class: "vpnfield" }, "VPN ", vpnCell(i, tun)));
  const toggle = el("button", { class: "small", "aria-expanded": String(open), onclick: () => {
    const on = more.hidden;
    more.hidden = !on;
    toggle.setAttribute("aria-expanded", String(on));
    on ? openConns.add(i.id) : openConns.delete(i.id);
  } }, "Settings");
  // Its tile: its colour, with what it fetches now inside (speedTile).
  const tile = el("span", { class: "tile num", "data-speed": i.id });
  tile.style.color = ifaceHue(i.id, latestState);
  return el("div", { class: "conn-row" + (i.present ? "" : " absent") }, tile,
    el("div", { class: "who" },
      el("b", {}, i.name, i.isMain ? badge("Main") : null, tun ? badge("VPN", TUNNEL_CLASS[tun.tunnelState]) : null),
      el("div", { class: "sub" }, i.present ? el("span", { class: "mono" }, i.ip) : "not connected", " · ", i.description),
      badges.length ? el("div", { class: "badges" }, badges) : null),
    el("span", { class: "badge st " + tone, "data-state": i.id }, word),
    el("label", { class: "use" }, check(i.enabled, "enabled", "Use"), " Use"),
    toggle, more);
}

function renderIfaces(state, settings) {
  const all = state.interfaces || [];
  const ifaces = all.filter((i) => !i.tunnel);
  const tunnels = new Map(all.filter((i) => i.tunnel).map((t) => [t.of, t]));
  const sig = JSON.stringify([all.map((i) => [i.id, i.present, i.enabled, i.connections, i.isMain, i.ip, i.upload, i.uploadLimit, i.downloadLimit, i.incoming, i.tunnelState, i.tunnelError, i.forwardedPort, i.ip6, i.families]), settings.mainInterface]);
  const warnings = routerWarnings(all);
  const hints = ipv6Hints(state);
  $("ipv6Hint").hidden = !hints.length;
  $("ipv6Hint").replaceChildren(...hints.map((t) => el("div", { class: "muted" }, t)));
  $("uplinkWarn").hidden = !warnings.length;
  $("uplinkWarn").replaceChildren(...warnings.map((t) => el("div", { class: "warn" }, t)));
  const body = $("ifaces");
  if (sig !== ifaceSig && !body.contains(document.activeElement)) {
    ifaceSig = sig;
    body.replaceChildren(...ifaces.map((i) => ifaceRow(i, tunnels.get(i.id), settings)));
    $("followWindows").checked = !settings.mainInterface;
  }
  // Speeds change every second: only the numbers, not the rows.
  for (const i of ifaces) {
    const c = body.querySelector(`[data-speed="${i.id}"]`);
    const t = tunnels.get(i.id);
    const total = (i.speed || 0) + ((t && t.speed) || 0);
    if (c) speedTile(c, total);
    const w = body.querySelector(`[data-state="${i.id}"]`);
    const [word, tone] = connState(i, t);
    if (w && w.textContent !== word) { w.textContent = word; w.className = "badge st " + tone; }
  }
}

// --- settings: saved on every change ---

for (const id of ["sVpnHttp", "sVpnTorrent"]) {
  $(id).replaceChildren(...VPN_MODES.map(([v, text]) => el("option", { value: v }, text)));
}

const FIELDS = {
  sDir: ["saveDir", (s) => s.saveDir, (v) => v.trim()],
  sThreshold: ["thresholdBytes", (s) => +(s.thresholdBytes / GB).toFixed(3), (v) => Math.round(Number(v) * GB)],
  sParallel: ["maxParallel", (s) => s.maxParallel, Number],
  sDownLimit: ["downloadLimit", (s) => (s.downloadLimit ? +(s.downloadLimit / MB).toFixed(3) : ""), (v) => Math.round((Number(v) || 0) * MB)],
  sUpLimit: ["uploadLimit", (s) => (s.uploadLimit ? +(s.uploadLimit / MB).toFixed(3) : ""), (v) => Math.round((Number(v) || 0) * MB)],
  sConns: ["defaultConnections", (s) => s.defaultConnections, Number],
  sEta: ["etaWindowSec", (s) => s.etaWindowSec, Number],
  sRatio: ["seedRatio", (s) => s.seedRatio, Number],
  sVpnHttp: ["vpnHttp", (s) => s.vpnHttp, String],
  sVpnTorrent: ["vpnTorrent", (s) => s.vpnTorrent, String],
};

for (const [id, [key, , parse]] of Object.entries(FIELDS)) {
  // Out of its min/max, or empty where a number is needed: say so, send nothing.
  $(id).addEventListener("change", () => $(id).reportValidity() && act("setSettings", { [key]: parse($(id).value) }, "Saved"));
}
$("sAuto").addEventListener("change", () => act("setAutostart", { on: $("sAuto").checked }, "Saved"));
$("sFolder").addEventListener("change", () => act("setSettings", { folderDefault: $("sFolder").checked }, "Saved"));
$("sParts").addEventListener("change", () => act("setSettings", { partsParallel: $("sParts").checked }, "Saved"));
$("sIPv6").addEventListener("change", () => act("setSettings", { ipv6: $("sIPv6").checked }, $("sIPv6").checked ? "IPv6 on" : "IPv6 off"));

// The accent: Follow Firefox, a preset, or any color (lib/accent.js).
const setAccent = (c, said) => act("setSettings", { accent: c }, said);
function renderAccents(s) {
  const chosen = s.accent || "";
  const custom = chosen && !ACCENT_PRESETS.some(([, c]) => c === chosen);
  const radio = (checked, attrs, ...kids) => el("button", { role: "radio", "aria-checked": String(checked), ...attrs }, ...kids);
  const picker = el("input", { type: "color", value: chosen || pageAccent(s) || "#1d4ed8", "aria-label": "Custom color" });
  picker.addEventListener("change", () => setAccent(picker.value, "Accent color saved"));
  $("accents").replaceChildren(
    radio(!chosen, { class: "follow", onclick: () => setAccent("", "Accent follows Firefox") }, "Follow Firefox"),
    ...ACCENT_PRESETS.map(([name, c]) => radio(chosen === c,
      { title: name, "aria-label": name, style: `background:${c}`, onclick: () => setAccent(c, `Accent: ${name}`) })),
    el("label", { title: "Custom color" }, picker, custom ? "Custom" : "Other…"));
}

// fillSettings shows what the engine has, except in the field being edited.
function fillSettings(s) {
  for (const [id, [, show]] of Object.entries(FIELDS)) {
    if (document.activeElement !== $(id)) $(id).value = show(s);
  }
  $("sFolder").checked = !!s.folderDefault;
  $("sParts").checked = !!s.partsParallel;
  $("sIPv6").checked = !!s.ipv6;

}

// --- VPN provider ---

// The engine keeps the chosen provider (settings.vpnProvider); being signed in to one wins.
let provider = "";
let piaSignedIn = false;

function showProvider(p) {
  provider = p;
  $("vpnProvider").value = p;
  $("piaBox").hidden = p !== "pia";
}

$("vpnProvider").addEventListener("change", async () => {
  const p = $("vpnProvider").value;
  // Asked in the page, like removing a download: no browser dialog.
  if (piaSignedIn && p !== "pia") {
    $("piaOut").hidden = false;
    return;
  }
  showProvider(p);
  await act("setSettings", { vpnProvider: p });
});
$("piaOutNo").onclick = () => {
  $("piaOut").hidden = true;
  showProvider("pia");
};
$("piaOutYes").onclick = async () => {
  const p = $("vpnProvider").value;
  $("piaOut").hidden = true;
  $("vpnProvider").focus(); // while it has the focus, renderPIA leaves the choice alone
  await act("clearPia", {}, "Signed out of PIA");
  showProvider(p);
  await act("setSettings", { vpnProvider: p });
};

// --- PIA ---

const PIA_STATE = {
  off: ["Not signed in", "dim"], connecting: ["Connecting", "warn"], ok: ["Connected", "ok"],
  "login-failed": ["Login refused: sign in again", "bad"], error: ["Trouble connecting", "warn"],
};
let piaRegionsLoaded = false;

function renderPIA(p, settings) {
  piaSignedIn = !!p.configured;
  if (piaSignedIn && provider !== "pia" && document.activeElement !== $("vpnProvider")) showProvider("pia");
  const [text, cls] = PIA_STATE[p.state] || [p.state, ""];
  $("piaState").textContent = text + (p.state === "ok" && p.region ? ` · ${p.region}` : "");
  $("piaState").className = "badge " + cls;
  $("piaError").textContent = p.state === "error" ? p.error || "" : "";
  $("piaClear").hidden = !p.configured;
  $("piaSave").textContent = p.configured ? "Save" : "Sign in";
  if (!piaRegionsLoaded) {
    piaRegionsLoaded = true;
    call("piaRegions", {}).then((regions) => {
      $("piaRegion").replaceChildren(el("option", { value: "" }, "Lowest latency"),
        ...regions.sort((a, b) => a.name.localeCompare(b.name))
          .map((r) => el("option", { value: r.id }, r.name + (r.portForward ? "" : " (no port forwarding)"))));
      $("piaRegion").value = settings.piaRegion || "";
    }).catch(() => { piaRegionsLoaded = false; });
  }
}

$("piaSave").onclick = () => {
  const user = $("piaUser").value.trim();
  if (user && !$("piaPass").value) return toast("Enter the password too");
  act("setPia", { user, pass: $("piaPass").value, region: $("piaRegion").value }, user ? "Signed in to PIA" : "Region saved")
    .then(() => { $("piaPass").value = ""; });
};
$("piaClear").onclick = () => act("clearPia", {}, "Signed out of PIA");

// --- learned sites ---

function renderRecipes(recipes) {
  const box = $("recipes");
  if (!Host.recipesHere) {
    if (!box.childElementCount) box.replaceChildren(el("div", { class: "muted" }, "Recipes are kept in Firefox, where they are learned and replayed."));
    return;
  }
  const sig = JSON.stringify(recipes);
  if (sig === recipesSig) return;
  recipesSig = sig;
  const hosts = Object.keys(recipes || {}).sort();
  if (!hosts.length) {
    box.replaceChildren(el("div", { class: "muted" }, "None yet. The first time a site needs it, DMS² asks you to click through once."));
    return;
  }
  box.replaceChildren(el("div", { class: "conn-scroll" }, el("table", { class: "recipes" },
    el("tbody", {}, hosts.map((h) => el("tr", {},
      el("td", {}, h),
      el("td", { class: "muted" }, recipes[h].steps.map((s) => s.text || s.tag).join(" → ") || "no clicks (the download starts by itself)"),
      el("td", { class: "muted when" }, new Date(recipes[h].learned).toLocaleDateString()),
      el("td", {}, el("button", { class: "small", onclick: () => Host.forgetRecipe(h).then(() => toast("Forgotten")) }, "Forget"))))))));
}

// speedTile shows a connection's speed in its tile: the number, the unit
// under it; hollow while it fetches nothing.
function speedTile(tile, speed) {
  const [n, unit] = speed > 0 ? fmtSpeed(speed).split(" ") : ["–", ""];
  if (tile.dataset.n === n + unit) return;
  tile.dataset.n = n + unit;
  tile.classList.toggle("idle", !(speed > 0));
  tile.replaceChildren(el("span", {}, n, unit ? el("small", {}, unit) : null));
  tile.title = speed > 0 ? fmtSpeed(speed) : "Fetching nothing now";
}

// --- the pages: Connections with the VPN, and Settings, each one list; the
// links under the tabs jump to a section and mark the one being read ---

const CONNECTIONS_PAGE = ["connections", "vpn", "pia"]; // #pia: the VPN's sign-in, from a torrent's card
const sections = [...document.querySelectorAll("main > section")];
const sectionLinks = [...$("sections").querySelectorAll("a")];

// found marks what a search finds in a section: the whole section when its
// title matches, else each group whose title does, else each setting whose
// label does; a group with nothing found goes too. Whether it found any.
function found(section, q) {
  const has = (node) => !!node && node.textContent.toLowerCase().includes(q);
  const all = !q || has(section.querySelector("h2"));
  let any = all;
  for (const g of section.querySelectorAll(".group")) {
    const whole = all || has(g.querySelector("h3"));
    let hits = whole;
    for (const row of g.querySelectorAll(".setting")) {
      const hit = whole || has(row.firstElementChild);
      row.classList.toggle("unfound", !hit);
      hits = hits || hit;
    }
    g.classList.toggle("unfound", !hits);
    any = any || hits;
  }
  for (const row of section.querySelectorAll(":scope > .fields > .setting")) {
    const hit = all || has(row.firstElementChild);
    row.classList.toggle("unfound", !hit);
    any = any || hit;
  }
  return any;
}

let page = ""; // "connections" or "settings", as last shown
function showPage() {
  const at = location.hash.slice(1);
  const q = $("find").value.trim().toLowerCase();
  const was = page;
  page = !at || CONNECTIONS_PAGE.includes(at) ? "connections" : "settings";
  const mine = (id) => CONNECTIONS_PAGE.includes(id) === (page === "connections");
  let shown = 0;
  for (const s of sections) {
    const on = q ? found(s, q) : mine(s.id);
    if (!q) found(s, "");
    s.hidden = !on;
    shown += on;
  }
  for (const a of $("pages").querySelectorAll("a")) {
    if (!q && (a.hash === "#connections") === (page === "connections")) a.setAttribute("aria-current", "page");
    else a.removeAttribute("aria-current");
  }
  for (const a of sectionLinks) a.hidden = !!q || a.dataset.page !== page;
  $("notFound").hidden = shown > 0;
  $("notFound").textContent = `No setting matches “${$("find").value.trim()}”.`;
  // Jump to the section asked for; a page's first section is its top.
  const target = $(at === "pia" ? "vpn" : at);
  if (q) scrollTo(0, 0);
  else if (target && target.previousElementSibling && !target.previousElementSibling.hidden) target.scrollIntoView();
  else if (page !== was || !target) scrollTo(0, 0);
  markSection();
}

// The section being read: the last whose top has passed under the header.
function markSection() {
  const mine = sections.filter((s) => !s.hidden);
  let cur = mine[0];
  for (const s of mine) if (s.getBoundingClientRect().top <= 120) cur = s;
  for (const a of sectionLinks) {
    if (cur && a.hash === "#" + cur.id) a.setAttribute("aria-current", "location");
    else a.removeAttribute("aria-current");
  }
}
addEventListener("scroll", markSection, { passive: true });
addEventListener("hashchange", showPage);
$("find").addEventListener("input", showPage);
// A section or page picked while searching shows it, not the search.
for (const a of [...sectionLinks, ...$("pages").querySelectorAll("a")]) {
  a.addEventListener("click", () => { if ($("find").value) { $("find").value = ""; if (a.hash === location.hash) showPage(); } });
}
showPage();

// --- wiring ---

connectUI((m) => {
  renderConn(m);
  if (!m.state || !m.settings) return;
  latestState = m.state;
  renderIfaces(m.state, m.settings);
  renderPIA(m.state.pia || { state: "off" }, m.settings);
  renderRecipes(m.recipes);
  if (JSON.stringify(m.settings) !== JSON.stringify(latestSettings)) {
    latestSettings = m.settings;
    fillSettings(m.settings);
    renderAccents(m.settings);
    if (!piaSignedIn && document.activeElement !== $("vpnProvider")) showProvider(m.settings.vpnProvider || "");
  }
  if (document.activeElement !== $("sAuto")) $("sAuto").checked = m.autostart;
});
