"use strict";

// Connections, settings and learned sites, on one page.

let ifaceSig = "";
let settingsFilled = false;
let recipesSig = "";

// --- connections ---

$("refreshIfaces").onclick = () => act("refreshInterfaces", {}, "Connections refreshed");

// Importing a .conf: the file picker is shared, this is the connection it's for.
let confFor = "";
$("confFile").addEventListener("change", async () => {
  const f = $("confFile").files[0];
  $("confFile").value = "";
  if (f) act("importTunnel", { iface: confFor, conf: await f.text() }, "VPN tunnel imported");
});

function vpnCell(i, tun) {
  if (!tun) {
    return el("button", { class: "small", onclick: () => { confFor = i.id; $("confFile").click(); } }, "Import .conf");
  }
  return el("span", { style: "white-space:nowrap" },
    el("span", { class: "badge " + (TUNNEL_CLASS[tun.tunnelState] || ""), title: tun.tunnelError || tun.ip }, TUNNEL_TEXT[tun.tunnelState] || tun.tunnelState), " ",
    el("button", { class: "small", onclick: () => act("removeTunnel", { iface: i.id }, "VPN tunnel removed") }, "Remove"));
}

function renderIfaces(state, settings) {
  const all = state.interfaces || [];
  const ifaces = all.filter((i) => !i.tunnel);
  const tunnels = new Map(all.filter((i) => i.tunnel).map((t) => [t.of, t]));
  const sig = JSON.stringify([all.map((i) => [i.id, i.present, i.enabled, i.connections, i.isMain, i.ip, i.upload, i.uploadLimit, i.downloadLimit, i.incoming, i.tunnelState, i.tunnelError]), settings.mainInterface]);
  const body = $("ifaces");
  if (sig !== ifaceSig && !body.contains(document.activeElement)) {
    ifaceSig = sig;
    body.replaceChildren();
    for (const i of ifaces) {
      const conns = el("input", { type: "number", min: 1, max: 32, value: i.connections, style: "width:70px" });
      conns.addEventListener("change", () => act("setInterface", { id: i.id, connections: Number(conns.value) }));
      const enabled = el("input", { type: "checkbox", checked: i.enabled });
      enabled.addEventListener("change", () => act("setInterface", { id: i.id, enabled: enabled.checked }));
      const upload = el("input", { type: "checkbox", checked: i.upload, title: "Let torrent peers download from you over this connection" });
      upload.addEventListener("change", () => act("setInterface", { id: i.id, upload: upload.checked }));
      const limit = (bps, key, title) => {
        const input = el("input", { type: "number", min: 0, step: "any", placeholder: "none", title, value: bps ? +(bps / MB).toFixed(3) : "", style: "width:70px" });
        input.addEventListener("change", () => act("setInterface", { id: i.id, [key]: Math.round((Number(input.value) || 0) * MB) }));
        return input;
      };
      const main = el("input", { type: "radio", name: "main", checked: settings.mainInterface === i.id });
      main.addEventListener("change", () => act("setSettings", { mainInterface: i.id }, `${i.name} is now the main connection`));
      body.append(el("tr", { style: i.present ? "" : "opacity:.5" },
        el("td", {}, el("div", {}, i.name), el("div", { class: "muted", style: "font-size:12px" }, i.description)),
        el("td", { class: "mono", style: "white-space:nowrap" }, i.present ? i.ip : "not connected"),
        el("td", {},
          i.isDefault ? el("span", { class: "badge dim" }, "Windows default") : null, " ",
          i.metered ? el("span", { class: "badge warn" }, "Metered") : null, " ",
          i.vpn ? el("span", { class: "badge warn" }, "VPN") : null, " ",
          i.isMain ? el("span", { class: "badge" }, "Main") : null, " ",
          i.incoming ? el("span", { class: "badge " + (i.incoming === "reachable" ? "ok" : "dim"), title: INCOMING_TITLE[i.incoming] },
            i.incoming === "reachable" ? "Peers can connect in" : "Outgoing only") : null),
        el("td", {}, enabled),
        el("td", {}, upload),
        el("td", {}, conns),
        el("td", { style: "white-space:nowrap" }, limit(i.downloadLimit, "downloadLimit", "Download limit"), " / ", limit(i.uploadLimit, "uploadLimit", "Upload limit")),
        el("td", {}, vpnCell(i, tunnels.get(i.id))),
        el("td", {}, main),
        el("td", { class: "num", "data-speed": i.id })));
    }
    const auto = el("input", { type: "radio", name: "main", checked: !settings.mainInterface });
    auto.addEventListener("change", () => act("setSettings", { mainInterface: "" }, "Main connection follows Windows"));
    body.append(el("tr", {}, el("td", { colspan: 8, class: "muted" }, "Use whatever Windows uses by default"), el("td", {}, auto), el("td")));
  }
  for (const i of ifaces) {
    const c = body.querySelector(`[data-speed="${i.id}"]`);
    if (c) c.textContent = i.speed > 0 ? fmtSpeed(i.speed) : "";
  }
}

// --- settings ---

const GB = 1e9;
const MB = 1e6;

function fillSettings(s, autostart) {
  $("sThreshold").value = +(s.thresholdBytes / GB).toFixed(3);
  $("sDir").value = s.saveDir;
  $("sParallel").value = s.maxParallel;
  $("sConns").value = s.defaultConnections;
  $("sEta").value = s.etaWindowSec;
  $("sRatio").value = s.seedRatio;
  for (const [id, value] of [["sVpnHttp", s.vpnHttp], ["sVpnTorrent", s.vpnTorrent]]) {
    $(id).replaceChildren(...VPN_MODES.map(([v, text]) => el("option", { value: v }, text)));
    $(id).value = value;
  }
  $("sAuto").checked = autostart;
  browser.storage.local.get("landingBack").then((v) => {
    $("sBack").value = Number.isInteger(v.landingBack) ? v.landingBack : 1;
  });
}

$("saveSettings").onclick = async () => {
  await act("setSettings", {
    thresholdBytes: Math.round(Number($("sThreshold").value) * GB),
    saveDir: $("sDir").value.trim(),
    maxParallel: Number($("sParallel").value),
    defaultConnections: Number($("sConns").value),
    etaWindowSec: Number($("sEta").value),
    seedRatio: Number($("sRatio").value),
    vpnHttp: $("sVpnHttp").value,
    vpnTorrent: $("sVpnTorrent").value,
  });
  const back = Math.min(5, Math.max(0, Math.round(Number($("sBack").value) || 0)));
  await browser.storage.local.set({ landingBack: back });
  await act("setAutostart", { on: $("sAuto").checked }, "Settings saved");
};

// --- learned sites ---

function renderRecipes(recipes) {
  const sig = JSON.stringify(recipes);
  if (sig === recipesSig) return;
  recipesSig = sig;
  const box = $("recipes");
  const hosts = Object.keys(recipes || {}).sort();
  if (!hosts.length) {
    box.replaceChildren(el("div", { class: "empty" }, "Nothing learned yet. The first time a site needs a fresh link per connection, DMS² asks you to click through once."));
    return;
  }
  box.replaceChildren(el("table", {},
    el("thead", {}, el("tr", {}, el("th", {}, "Site"), el("th", {}, "Clicks"), el("th", {}, "Learned"), el("th"))),
    el("tbody", {}, hosts.map((h) => el("tr", {},
      el("td", {}, h),
      el("td", { class: "muted" }, recipes[h].steps.map((s) => s.text || s.tag).join(" → ") || "none (download starts by itself)"),
      el("td", { class: "muted" }, new Date(recipes[h].learned).toLocaleDateString()),
      el("td", {}, el("button", { class: "small", onclick: () => browser.runtime.sendMessage({ type: "forgetRecipe", host: h }).then(() => toast("Forgotten")) }, "Forget")))))));
}

// --- wiring ---

connectUI((m) => {
  renderConn(m, "Engine connected");
  if (!m.state || !m.settings) return;
  renderIfaces(m.state, m.settings);
  renderRecipes(m.recipes);
  if (!settingsFilled) {
    fillSettings(m.settings, m.autostart);
    settingsFilled = true;
  }
});
