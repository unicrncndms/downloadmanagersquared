"use strict";

// Connections, settings and learned sites, on one page.

let ifaceSig = "";
let settingsFilled = false;
let recipesSig = "";

// --- connections ---

$("refreshIfaces").onclick = () => act("refreshInterfaces", {}, "Connections refreshed");

function renderIfaces(state, settings) {
  const ifaces = state.interfaces || [];
  const sig = JSON.stringify([ifaces.map((i) => [i.id, i.present, i.enabled, i.connections, i.isMain, i.ip]), settings.mainInterface]);
  const body = $("ifaces");
  if (sig !== ifaceSig && !body.contains(document.activeElement)) {
    ifaceSig = sig;
    body.replaceChildren();
    for (const i of ifaces) {
      const conns = el("input", { type: "number", min: 1, max: 32, value: i.connections, style: "width:70px" });
      conns.addEventListener("change", () => act("setInterface", { id: i.id, connections: Number(conns.value) }));
      const enabled = el("input", { type: "checkbox", checked: i.enabled });
      enabled.addEventListener("change", () => act("setInterface", { id: i.id, enabled: enabled.checked }));
      const main = el("input", { type: "radio", name: "main", checked: settings.mainInterface === i.id });
      main.addEventListener("change", () => act("setSettings", { mainInterface: i.id }, `${i.name} is now the main connection`));
      body.append(el("tr", { style: i.present ? "" : "opacity:.5" },
        el("td", {}, el("div", {}, i.name), el("div", { class: "muted", style: "font-size:12px" }, i.description)),
        el("td", { class: "mono" }, i.present ? i.ip : "not connected"),
        el("td", {},
          i.isDefault ? el("span", { class: "badge dim" }, "Windows default") : null, " ",
          i.metered ? el("span", { class: "badge warn" }, "Metered") : null, " ",
          i.vpn ? el("span", { class: "badge warn" }, "VPN") : null, " ",
          i.isMain ? el("span", { class: "badge" }, "Main") : null),
        el("td", {}, enabled),
        el("td", {}, conns),
        el("td", {}, main),
        el("td", { class: "num", "data-speed": i.id })));
    }
    const auto = el("input", { type: "radio", name: "main", checked: !settings.mainInterface });
    auto.addEventListener("change", () => act("setSettings", { mainInterface: "" }, "Main connection follows Windows"));
    body.append(el("tr", {}, el("td", { colspan: 5, class: "muted" }, "Use whatever Windows uses by default"), el("td", {}, auto), el("td")));
  }
  for (const i of ifaces) {
    const c = body.querySelector(`[data-speed="${i.id}"]`);
    if (c) c.textContent = i.speed > 0 ? fmtSpeed(i.speed) : "";
  }
}

// --- settings ---

const GB = 1e9;

function fillSettings(s, autostart) {
  $("sThreshold").value = +(s.thresholdBytes / GB).toFixed(3);
  $("sDir").value = s.saveDir;
  $("sParallel").value = s.maxParallel;
  $("sConns").value = s.defaultConnections;
  $("sEta").value = s.etaWindowSec;
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
