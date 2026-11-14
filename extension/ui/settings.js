"use strict";

// Two pages in one: Connections (with the VPN), and Settings, chosen by the
// address's #. Every change is saved as soon as it's made.

const MB = 1e6;
// Typed decimals: a point or a comma, whatever the system's locale.
const DECIMAL = "[0-9]*[.,]?[0-9]*";
const dec = (v) => Number(String(v).replace(",", "."));

let ifaceSig = "";
let recipesSig = "";
let latestSettings = null;
let latestState = {};

windowHeader();

// --- connections ---

// The engine looks for changes itself every few seconds; this looks now.
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
$("mainSelect").addEventListener("change", () => {
  const id = $("mainSelect").value;
  const name = id && $("mainSelect").selectedOptions[0].textContent;
  act("setSettings", { mainInterface: id }, id ? `${name} is now the main connection` : "Main connection follows Windows");
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

// One line of the VPN section: a connection with its tunnel, or the way to import one.
function tunnelRow(i, tun) {
  const pia = tun && tun.provider === "pia";
  // The port the VPN provider forwards to this tunnel, where it can't tell DMS² itself.
  let port = null;
  if (tun && !pia) {
    port = el("input", { type: "number", class: "port", min: 1, max: 65535, placeholder: "none", "aria-label": `Forwarded port: ${i.name}`, value: tun.forwardedPort || "" });
    port.addEventListener("change", () => {
      const n = Number(port.value) || 0;
      act("setForwardedPort", { iface: i.id, port: n }, n ? `Forwarded port ${n} set` : "Forwarded port cleared");
    });
  }
  return el("div", { class: "tunnel-row" },
    el("b", {}, i.name),
    tun ? el("div", { class: "vpn" },
      badge((pia ? "PIA · " : "") + (TUNNEL_TEXT[tun.tunnelState] || tun.tunnelState).replace(/^VPN /, ""), TUNNEL_CLASS[tun.tunnelState], tun.ip),
      tun.tunnelError ? el("span", { class: "muted tunnel-error" }, tun.tunnelError) : null)
      : el("span", { class: "muted" }, "No tunnel"),
    port && el("label", {}, "Forwarded port ", port,
      el("span", { class: "info", tabindex: "0", "data-tip": "A port your VPN provider forwards to this connection, for torrent peers to connect in (empty: none, or found by NAT-PMP). AirVPN: leave the port's Local field empty, and with several connections give each its own port, linked to that connection's device." })),
    pia ? null : tun
      ? el("button", { class: "small", onclick: () => act("removeTunnel", { iface: i.id }, "VPN tunnel removed") }, "Remove")
      : el("button", { class: "small", onclick: () => { confFor = i.id; $("confFile").click(); } }, "Import .conf"));
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
    const input = el("input", { type: "text", inputmode: "decimal", pattern: DECIMAL, class: "dec", placeholder: "–", title: title + " (empty: none)", "aria-label": `${title} in ${speedUnitLabel()}: ${i.name}`, value: unitField(bps) });
    input.addEventListener("change", () => input.reportValidity() && set({ [key]: speedFromUnit(dec(input.value) || 0) }));
    return input;
  };
  // The colour it is shown in everywhere: click its tile to pick it, from the
  // same swatches as the accent.
  const shown = Number.isInteger(i.color) ? i.color : 0;
  const colours = el("div", { class: "swatches", role: "radiogroup", "aria-label": `Colour: ${i.name}`, hidden: true },
    IFACE_COLORS.map((name, n) => el("button", { role: "radio", "aria-checked": String(n === shown), title: name, "aria-label": name, style: `background:var(--c${n})`,
      onclick: () => set({ color: n }, `${i.name} is now ${name}`) })));
  const label = el("input", { type: "text", class: "name", maxlength: 40, placeholder: i.windowsName || i.name, value: i.name === i.windowsName ? "" : i.name, "aria-label": `Name: ${i.windowsName || i.name}` });
  label.addEventListener("change", () => set({ label: label.value }, label.value.trim() ? "Name saved" : "Windows' name restored"));

  // In this order: what it is, its role, then what torrents get from it.
  const badges = [
    familyText(i) ? badge(familyText(i), "dim", i.ip6 && i.ip6 !== i.ip ? i.ip6 : undefined) : null,
    i.vpn ? badge("System VPN", "warn") : null,
    i.isMain ? badge("Main") : null,
    i.isDefault ? badge("Windows default") : null,
    i.incoming ? badge("Torrents: " + (i.incoming === "reachable" ? "Reachable" : "Outgoing only"), i.incoming === "reachable" ? "ok" : "dim", INCOMING_TITLE[i.incoming]) : null,
  ].filter(Boolean);
  // First what it is and does now; its settings fold under it.
  const [word, tone] = connState(i, tun);
  const open = openConns.has(i.id);
  const more = el("div", { class: "conn-more", hidden: !open },
    el("label", {}, "Requests at once ", conns,
      el("span", { class: "info", tabindex: "0", "data-tip": "How many parts of a download this connection fetches at the same time." })),
    el("label", {}, "Name ", label),
    el("label", {}, check(i.upload, "upload", "Seed torrents over this connection"), " Seed torrents over this connection"),
    el("label", {}, `Download limit (${speedUnitLabel()}) `, limit(i.downloadLimit, "downloadLimit", "Download limit")),
    el("label", {}, `Upload limit (${speedUnitLabel()}) `, limit(i.uploadLimit, "uploadLimit", "Upload limit")),
    el("a", { href: "#vpn" }, tun ? "VPN tunnel: see VPN" : "Import a VPN .conf: see VPN"),
    i.present ? null : el("button", { class: "small", onclick: () => act("forgetInterface", { id: i.id }, `${i.name} forgotten`) }, "Forget"));
  const toggle = el("button", { class: "small", "aria-expanded": String(open), onclick: () => {
    const on = more.hidden;
    more.hidden = !on;
    toggle.setAttribute("aria-expanded", String(on));
    on ? openConns.add(i.id) : openConns.delete(i.id);
  } }, "Options");
  // Its tile: its colour, with what it fetches now inside (speedTile); a click picks the colour.
  const tile = el("span", { class: "tile num", "data-speed": i.id, role: "button", tabindex: "0", "aria-label": `Colour: ${i.name}. Pick another`,
    onclick: () => { colours.hidden = !colours.hidden; },
    onkeydown: (ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); tile.click(); } } });
  tile.style.color = ifaceHue(i.id, latestState);
  return el("div", { class: "conn-row" + (i.present ? "" : " absent") }, tile,
    el("div", { class: "who" },
      el("b", {}, i.name, tun ? badge("VPN", TUNNEL_CLASS[tun.tunnelState]) : null),
      el("div", { class: "sub" }, i.present ? el("span", { class: "mono" }, i.ip) : "not connected", " · ", i.windowsName && i.windowsName !== i.name ? i.windowsName + " · " : "", i.description),
      badges.length ? el("div", { class: "badges" }, badges) : null,
      i.vpn && !i.enabled && i.present ? el("div", { class: "sub" }, "Off because this is a VPN running in Windows. Turn it on to download through it as an extra connection.") : null,
      colours),
    el("span", { class: "badge st " + tone, "data-state": i.id }, word),
    el("label", { class: "use" }, check(i.enabled, "enabled", "Use"), " Use"),
    toggle, more);
}

let tunnelSig = "";
function showTunnels(ifaces, tunnels, settings) {
  const box = $("tunnelBox");
  box.hidden = !tunnelsShown(settings.vpnProvider, tunnels.size);
  const sig = JSON.stringify([ifaces.map((i) => { const t = tunnels.get(i.id); return [i.id, i.name, t && [t.provider, t.tunnelState, t.tunnelError, t.ip, t.forwardedPort]]; })]);
  if (sig === tunnelSig || $("tunnelList").contains(document.activeElement)) return;
  tunnelSig = sig;
  $("tunnelList").replaceChildren(...ifaces.filter((i) => i.present || tunnels.has(i.id)).map((i) => tunnelRow(i, tunnels.get(i.id))));
}

function renderIfaces(state, settings) {
  const all = state.interfaces || [];
  const ifaces = all.filter((i) => !i.tunnel);
  const tunnels = new Map(all.filter((i) => i.tunnel).map((t) => [t.of, t]));
  const sig = JSON.stringify([all.map((i) => [i.id, i.present, i.enabled, i.connections, i.color, i.windowsName, i.isMain, i.isDefault, i.name, i.ip, i.upload, i.uploadLimit, i.downloadLimit, i.incoming, i.tunnelState, i.tunnelError, i.forwardedPort, i.ip6, i.families]), settings.mainInterface, speedUnitLabel()]);
  const warnings = routerWarnings(all);
  const hints = ipv6Hints(state);
  $("ipv6Hint").hidden = !hints.length;
  $("ipv6Hint").replaceChildren(...hints.map((t) => el("div", { class: "muted" }, t)));
  $("uplinkWarn").hidden = !warnings.length;
  $("uplinkWarn").replaceChildren(...warnings.map((t) => el("div", { class: "warn" }, t)));
  showTunnels(ifaces, tunnels, settings);
  const body = $("ifaces");
  if (sig !== ifaceSig && !body.contains(document.activeElement)) {
    ifaceSig = sig;
    body.replaceChildren(...ifaces.map((i) => ifaceRow(i, tunnels.get(i.id), settings)));
    const dflt = ifaces.find((i) => i.isDefault);
    $("mainSelect").replaceChildren(
      el("option", { value: "" }, dflt ? `Automatic (Windows' choice: ${dflt.name})` : "Automatic (Windows' choice)"),
      // Only a connection that is on and present can be main (the engine ignores any other);
      // the saved choice stays shown, marked, when it is not one.
      ...ifaces.filter((i) => (i.present && i.enabled) || i.id === settings.mainInterface)
        .map((i) => el("option", { value: i.id }, i.present && i.enabled ? i.name : `${i.name} (${i.present ? "off" : "not connected"})`)));
    $("mainSelect").value = settings.mainInterface || "";
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
$("sSpeedUnit").replaceChildren(el("option", { value: "MBps" }, "MB/s"), el("option", { value: "Mbps" }, "Mbit/s"));
$("sKeep").replaceChildren(...KEEP_CHOICES.map(([v, text]) => el("option", { value: v }, text)));

const FIELDS = {
  sDir: ["saveDir", (s) => s.saveDir, (v) => v.trim()],
  sThreshold: ["thresholdBytes", (s) => +(s.thresholdBytes / MB).toFixed(3), (v) => Math.max(MB, Math.round(dec(v) * MB))],
  sParallel: ["maxParallel", (s) => s.maxParallel, Number],
  sDownLimit: ["downloadLimit", (s) => unitField(s.downloadLimit), (v) => speedFromUnit(dec(v) || 0)],
  sUpLimit: ["uploadLimit", (s) => unitField(s.uploadLimit), (v) => speedFromUnit(dec(v) || 0)],
  sSpeedUnit: ["speedUnit", (s) => (s.speedUnit === "Mbps" ? "Mbps" : "MBps"), String],
  sConns: ["defaultConnections", (s) => s.defaultConnections, Number],
  sEta: ["etaWindowSec", (s) => s.etaWindowSec, Number],
  sNever: ["neverCapture", (s) => (s.neverCapture || []).join(", "), (v) => v.split(/[\s,;]+/).filter(Boolean)],
  sTorrentPort: ["torrentPort", (s) => s.torrentPort, Number],
  sTheme: ["theme", (s) => s.theme || "", String],
  sRatio: ["seedRatio", (s) => s.seedRatio, dec],
  sVpnHttp: ["vpnHttp", (s) => s.vpnHttp, String],
  sVpnTorrent: ["vpnTorrent", (s) => s.vpnTorrent, String],
  sKeep: ["archiveKeeping", (s) => s.archiveKeeping || "auto", String],
};

// The queue window: a start and an end per weekday, Monday first; the engine's index is Sunday = 0.
const WEEKDAYS = ["Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"];
const dayIndex = (n) => (n + 1) % 7;
const winInputs = WEEKDAYS.map((day) => ["start", "end"].map((k) => el("input", { type: "time", "aria-label": `${day} ${k === "start" ? "from" : "until"}` })));
$("qWindow").replaceChildren(...WEEKDAYS.map((day, n) => el("div", {}, el("span", { style: "display:inline-block;min-width:6em" }, day), winInputs[n][0], " to ", winInputs[n][1])));
for (const [a, b] of winInputs) for (const i of [a, b]) i.addEventListener("change", () => {
  // A day with only one time is not a window yet: nothing is sent until both are set or both cleared.
  if (!a.value !== !b.value) return;
  const week = Array.from({ length: 7 }, () => ({ start: "", end: "" }));
  winInputs.forEach(([x, y], n) => (week[dayIndex(n)] = { start: x.value, end: y.value }));
  act("setSettings", { queueWindow: week }, "Saved");
});
function fillWindow(s) {
  WEEKDAYS.forEach((_, n) => {
    const w = (s.queueWindow || [])[dayIndex(n)] || {};
    winInputs[n][0].value = w.start || "";
    winInputs[n][1].value = w.end || "";
  });
}

// The checkboxes: the setting each one holds, and whether it is on when the engine sends none.
const CHECKS = { sFolder: ["folderDefault", false], sParts: ["partsParallel", false], sNotify: ["notifyDone", true], sAwake: ["keepAwake", true] };

// The download folder is checked by the engine: what it refuses is said at the field.
function dirError(text) {
  $("sDirErr").hidden = !text;
  $("sDirErr").textContent = text || "";
  $("sDir").setAttribute("aria-invalid", String(!!text));
}
$("sDir").addEventListener("input", () => dirError(""));
$("sDir").addEventListener("change", async () => {
  try {
    await call("setSettings", { saveDir: $("sDir").value.trim() });
    dirError("");
    toast("Saved");
  } catch (e) {
    dirError(e.message);
  }
});

for (const [id, [key, , parse]] of Object.entries(FIELDS).filter(([id]) => id !== "sDir")) {
  // Out of its min/max, or empty where a number is needed: say so, send nothing.
  $(id).addEventListener("change", () => $(id).reportValidity() && act("setSettings", { [key]: parse($(id).value) }, "Saved"));
}
$("sVpnHttp").addEventListener("change", () => { $("vpnOnlyWarn").hidden = $("sVpnHttp").value !== "vpn"; });
$("sAuto").addEventListener("change", () => act("setAutostart", { on: $("sAuto").checked }, "Saved"));
for (const [id, [key]] of Object.entries(CHECKS)) $(id).addEventListener("change", () => act("setSettings", { [key]: $(id).checked }, "Saved"));
// The engine shows Windows' folder picker and saves the choice; the field follows the settings it sends.
$("sBrowse").addEventListener("click", () => act("pickSaveFolder", {}));
$("sIPv6").addEventListener("change", () => act("setSettings", { ipv6: $("sIPv6").checked }, $("sIPv6").checked ? "IPv6 on" : "IPv6 off"));

// The accent: Follow the browser's theme, a preset, or any color (lib/accent.js).
const setAccent = (c, said) => act("setSettings", { accent: c }, said);
function renderAccents(s) {
  const chosen = s.accent || "";
  const custom = chosen && !ACCENT_PRESETS.some(([, c]) => c === chosen);
  const radio = (checked, attrs, ...kids) => el("button", { role: "radio", "aria-checked": String(checked), ...attrs }, ...kids);
  const picker = el("input", { type: "color", value: chosen || pageAccent(s) || "#1d4ed8", "aria-label": "Custom colour" });
  picker.addEventListener("change", () => setAccent(picker.value, "Accent colour saved"));
  $("accents").replaceChildren(
    radio(!chosen, { class: "follow", onclick: () => setAccent("", "Accent follows the browser's theme") }, "Follow the browser's theme"),
    ...ACCENT_PRESETS.map(([name, c]) => radio(chosen === c,
      { title: name, "aria-label": name, style: `background:${c}`, onclick: () => setAccent(c, `Accent: ${name}`) })),
    el("label", { title: "Custom colour" }, picker, custom ? "Custom" : "Other…"));
}

// fillSettings shows what the engine has, except in the field being edited.
function fillSettings(s) {
  dirError(""); // the field below shows what is saved
  for (const [id, [, show]] of Object.entries(FIELDS)) {
    if (document.activeElement !== $(id)) $(id).value = show(s);
  }
  for (const [id, [key, on]] of Object.entries(CHECKS)) $(id).checked = on ? s[key] !== false : !!s[key];
  $("sIPv6").checked = !!s.ipv6;
  if (!$("qWindow").contains(document.activeElement)) fillWindow(s);
  for (const e of document.querySelectorAll(".speedUnit")) e.textContent = speedUnitLabel();
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
  $("piaSave").hidden = !!p.configured; // signed in: the region saves itself, a new login needs signing out first
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
$("piaRegion").addEventListener("change", () => piaSignedIn && act("setPia", { user: "", pass: "", region: $("piaRegion").value }, "Region saved"));
$("piaClear").onclick = () => act("clearPia", {}, "Signed out of PIA");

// --- learned sites ---

// Learned sites are kept by the browser that learned them, so only its pages list them.
// The window says which browsers have an extension connected (the engine knows).
function renderRecipes(recipes) {
  const box = $("recipes");
  if (!Host.recipesHere) {
    if (box.childElementCount) return;
    box.replaceChildren(el("div", { class: "muted" }, "Learned sites are kept in your browser, where they are learned and used. Open DMS² settings in the browser to see or forget them."));
    call("extensionBrowsers", {}).then((exes) => {
      const names = browserNames(exes);
      if (names.length) box.replaceChildren(el("div", { class: "muted" }, `Learned sites are kept in ${names.join(" and ")}, where they are learned and used. Open DMS² settings there to see or forget them.`));
    }).catch(() => {});
    return;
  }
  latestRecipes = recipes;
  const shown = Object.fromEntries(Object.entries(recipes || {}).filter(([h]) => !forgetting.has(h)));
  const sig = JSON.stringify(shown);
  if (sig === recipesSig) return;
  recipesSig = sig;
  recipes = shown;
  const hosts = Object.keys(recipes).sort();
  if (!hosts.length) {
    box.replaceChildren(el("div", { class: "muted" }, "None yet. The first time a site needs it, DMS² asks you to click through once."));
    return;
  }
  box.replaceChildren(el("div", { class: "conn-scroll" }, el("table", { class: "recipes" },
    el("tbody", {}, hosts.map((h) => el("tr", {},
      el("td", {}, h),
      el("td", { class: "muted" }, recipes[h].steps.map((s) => s.text || s.tag).join(" → ") || "no clicks (the download starts by itself)"),
      el("td", { class: "muted when" }, new Date(recipes[h].learned).toLocaleDateString()),
      el("td", {}, el("button", { class: "small", onclick: () => forgetSite(h) }, "Forget"))))))));
}

// Forgetting waits for the Undo toast to end: the row goes now, the browser forgets the site after.
const forgetting = new Set();
let latestRecipes = null;
function forgetSite(h) {
  forgetting.add(h);
  renderRecipes(latestRecipes);
  const t = $("toast");
  toast(`Forgot ${h}`);
  const end = () => t.classList.remove("show");
  const timer = setTimeout(() => {
    end();
    Host.forgetRecipe(h).catch((e) => toast(e.message, true)).finally(() => { forgetting.delete(h); renderRecipes(latestRecipes); });
  }, 8000);
  t.append(" ", el("button", { class: "small", onclick: () => { clearTimeout(timer); forgetting.delete(h); end(); renderRecipes(latestRecipes); } }, "Undo"));
  clearTimeout(toast.timer); // toast() would close it sooner
  toast.timer = setTimeout(end, 8000);
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
  // What a row says: its text, its (i) notes and the names of its controls.
  const said = (node) => (node.textContent + " " + [...node.querySelectorAll("[data-tip],[aria-label],[title]")]
    .map((e) => (e.dataset.tip || "") + " " + (e.getAttribute("aria-label") || "") + " " + (e.title || "")).join(" ")).toLowerCase().replace(/colour/g, "color");
  q = q.replace(/colour/g, "color");
  const has = (node) => !!node && said(node).includes(q);
  const all = !q || has(section.querySelector("h2"));
  let any = all;
  for (const g of section.querySelectorAll(".group")) {
    const whole = all || has(g.querySelector("h3, summary"));
    let hits = whole;
    for (const row of g.querySelectorAll(".setting")) {
      const hit = whole || has(row.firstElementChild);
      row.classList.toggle("unfound", !hit);
      hits = hits || hit;
    }
    g.classList.toggle("unfound", !hits);
    any = any || hits;
  }
  for (const row of section.querySelectorAll("#ifaces .conn-row")) {
    const hit = all || has(row);
    row.classList.toggle("unfound", !hit);
    any = any || hit;
  }
  for (const row of section.querySelectorAll(":scope > .setting, :scope > .fields > .setting")) {
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
  // A folded group opens when the search finds something in it.
  if (q) for (const d of document.querySelectorAll("details.adv")) d.open = !!d.querySelector(".setting:not(.unfound)");
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

// --- about and restore ---

// Guide and release notes leave DMS² through the host, like any link.
for (const a of document.querySelectorAll("a[data-guide], a[data-releases]")) {
  const url = "guide" in a.dataset ? GUIDE_URL + (a.dataset.guide ? "#" + a.dataset.guide : "") : RELEASES_URL;
  a.href = url;
  a.addEventListener("click", (e) => { e.preventDefault(); Host.openUrl(url); });
}
$("openLog").onclick = () => act("openLog", {});
$("openData").onclick = () => act("openDataFolder", {});
$("restoreAsk").onclick = () => { $("restoreBox").hidden = false; };
$("restoreNo").onclick = () => { $("restoreBox").hidden = true; };
$("restoreYes").onclick = async () => {
  $("restoreBox").hidden = true;
  await act("restoreSettings", {}, "Settings restored to their defaults");
};
// In the extension the engine and the extension are versions of their own; in the window there is one.
function renderAbout(m) {
  const engine = m.engineVersion || "unknown";
  $("aboutVersion").textContent = Host.openDownloads ? `DMS² ${engine}` : `Engine ${engine} · Extension ${Host.version}`;
}
if (Host.openDownloads) $("sThresholdText").textContent = "Take over downloads above (MB)";

// --- wiring ---

connectUI((m) => {
  renderConn(m);
  renderAbout(m);
  if (!m.state || !m.settings) return;
  latestState = m.state;
  renderIfaces(m.state, m.settings);
  renderPIA(m.state.pia || { state: "off" }, m.settings);
  renderRecipes(m.recipes);
  if (JSON.stringify(m.settings) !== JSON.stringify(latestSettings)) {
    latestSettings = m.settings;
    fillSettings(m.settings);
    $("vpnOnlyWarn").hidden = m.settings.vpnHttp !== "vpn";
    renderAccents(m.settings);
    if (!piaSignedIn && document.activeElement !== $("vpnProvider")) showProvider(m.settings.vpnProvider || "");
  }
  if (document.activeElement !== $("sAuto")) $("sAuto").checked = m.autostart;
});
