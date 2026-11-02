"use strict";

// Connections, settings, VPN and learned sites, on one page. Every change
// is saved as soon as it's made.

const GB = 1e9;
const MB = 1e6;

let ifaceSig = "";
let recipesSig = "";
let latestSettings = null;
let latestState = {};

// --- connections ---

$("refreshIfaces").onclick = () => act("refreshInterfaces", {}, "Connections refreshed");
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
  return el("div", { class: "vpn" },
    badge((pia ? "PIA · " : "") + (TUNNEL_TEXT[tun.tunnelState] || tun.tunnelState).replace(/^VPN /, ""),
      TUNNEL_CLASS[tun.tunnelState], tun.tunnelError || tun.ip),
    pia ? null : el("button", { class: "small", onclick: () => act("removeTunnel", { iface: i.id }, "VPN tunnel removed") }, "Remove"));
}

function ifaceRow(i, tun, settings) {
  const set = (patch, ok) => act("setInterface", Object.assign({ id: i.id }, patch), ok);
  const check = (on, key) => {
    const c = el("input", { type: "checkbox", checked: on });
    c.addEventListener("change", () => set({ [key]: c.checked }));
    return c;
  };
  const conns = el("input", { type: "number", min: 1, max: 32, value: i.connections });
  conns.addEventListener("change", () => set({ connections: Number(conns.value) }));
  const limit = (bps, key, title) => {
    const input = el("input", { type: "number", min: 0, step: "any", placeholder: "–", title: title + " (empty: none)", value: bps ? +(bps / MB).toFixed(3) : "" });
    input.addEventListener("change", () => set({ [key]: Math.round((Number(input.value) || 0) * MB) }));
    return input;
  };
  // Shows the main connection in effect, chosen or Windows'.
  const main = el("input", { type: "radio", name: "main", checked: i.isMain });
  main.addEventListener("change", () => act("setSettings", { mainInterface: i.id }, `${i.name} is now the main connection`));

  // What the connection is, then the few facts worth a badge.
  const badges = [
    i.isDefault ? badge("Windows default") : null,
    i.metered ? badge("Metered", "warn") : null,
    i.vpn ? badge("System VPN", "warn") : null,
    i.incoming ? badge(i.incoming === "reachable" ? "Reachable" : "Outgoing only", i.incoming === "reachable" ? "ok" : "dim", INCOMING_TITLE[i.incoming]) : null,
  ].filter(Boolean);
  return el("div", { class: "conn-row" + (i.present ? "" : " absent") },
    el("div", { class: "who" },
      el("b", {}, i.name),
      el("div", { class: "sub" }, i.present ? el("span", { class: "mono" }, i.ip) : "not connected", " · ", i.description),
      badges.length ? el("div", { class: "badges" }, badges) : null),
    el("span", { class: "center" }, check(i.enabled, "enabled")),
    el("span", { class: "center" }, main),
    conns,
    el("span", { class: "center" }, check(i.upload, "upload")),
    el("span", { class: "limits" }, "↓", limit(i.downloadLimit, "downloadLimit", "Download limit"), "↑", limit(i.uploadLimit, "uploadLimit", "Upload limit")),
    vpnCell(i, tun),
    el("span", { class: "speed num", "data-speed": i.id }));
}

function renderIfaces(state, settings) {
  const all = state.interfaces || [];
  const ifaces = all.filter((i) => !i.tunnel);
  const tunnels = new Map(all.filter((i) => i.tunnel).map((t) => [t.of, t]));
  const sig = JSON.stringify([all.map((i) => [i.id, i.present, i.enabled, i.connections, i.isMain, i.ip, i.upload, i.uploadLimit, i.downloadLimit, i.incoming, i.tunnelState, i.tunnelError]), settings.mainInterface]);
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
    if (c) c.textContent = total > 0 ? fmtSpeed(total) : "";
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
  sConns: ["defaultConnections", (s) => s.defaultConnections, Number],
  sEta: ["etaWindowSec", (s) => s.etaWindowSec, Number],
  sRatio: ["seedRatio", (s) => s.seedRatio, Number],
  sVpnHttp: ["vpnHttp", (s) => s.vpnHttp, String],
  sVpnTorrent: ["vpnTorrent", (s) => s.vpnTorrent, String],
};

for (const [id, [key, , parse]] of Object.entries(FIELDS)) {
  $(id).addEventListener("change", () => act("setSettings", { [key]: parse($(id).value) }, "Saved"));
}
$("sBack").addEventListener("change", async () => {
  const back = Math.min(5, Math.max(0, Math.round(Number($("sBack").value) || 0)));
  await browser.storage.local.set({ landingBack: back });
  $("sBack").value = back;
  toast("Saved");
});
$("sAuto").addEventListener("change", () => act("setAutostart", { on: $("sAuto").checked }, "Saved"));

// fillSettings shows what the engine has, except in the field being edited.
function fillSettings(s) {
  for (const [id, [, show]] of Object.entries(FIELDS)) {
    if (document.activeElement !== $(id)) $(id).value = show(s);
  }
}

browser.storage.local.get("landingBack").then((v) => {
  $("sBack").value = Number.isInteger(v.landingBack) ? v.landingBack : 1;
});

// --- PIA ---

const PIA_STATE = {
  off: ["Not signed in", "dim"], connecting: ["Connecting", "warn"], ok: ["Connected", "ok"],
  "login-failed": ["Login refused: sign in again", "bad"], error: ["Trouble connecting", "warn"],
};
let piaRegionsLoaded = false;

function renderPIA(p, settings) {
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
  const sig = JSON.stringify(recipes);
  if (sig === recipesSig) return;
  recipesSig = sig;
  const box = $("recipes");
  const hosts = Object.keys(recipes || {}).sort();
  if (!hosts.length) {
    box.replaceChildren(el("div", { class: "muted" }, "None yet. The first time a site needs it, DMS² asks you to click through once."));
    return;
  }
  box.replaceChildren(el("div", { class: "conn-scroll" }, el("table", { style: "width:100%;border-collapse:collapse;font-size:13px" },
    el("tbody", {}, hosts.map((h) => el("tr", { style: "border-top:1px solid var(--line)" },
      el("td", { style: "padding:6px 8px 6px 0" }, h),
      el("td", { class: "muted", style: "padding:6px 8px" }, recipes[h].steps.map((s) => s.text || s.tag).join(" → ") || "no clicks (the download starts by itself)"),
      el("td", { class: "muted", style: "padding:6px 8px;white-space:nowrap" }, new Date(recipes[h].learned).toLocaleDateString()),
      el("td", { style: "padding:6px 0;text-align:right" }, el("button", { class: "small", onclick: () => browser.runtime.sendMessage({ type: "forgetRecipe", host: h }).then(() => toast("Forgotten")) }, "Forget"))))))));
}

// --- wiring ---

connectUI((m) => {
  renderConn(m, "Engine connected");
  if (!m.state || !m.settings) return;
  latestState = m.state;
  renderIfaces(m.state, m.settings);
  renderPIA(m.state.pia || { state: "off" }, m.settings);
  renderRecipes(m.recipes);
  if (JSON.stringify(m.settings) !== JSON.stringify(latestSettings)) {
    latestSettings = m.settings;
    fillSettings(m.settings);
  }
  if (document.activeElement !== $("sAuto")) $("sAuto").checked = m.autostart;
});
