"use strict";

// The downloads view: the toolbar popup, or a small window when opened from the tray.

let latest = {};
const cards = new Map(); // download id -> card controller

if (new URLSearchParams(location.search).has("window")) document.body.classList.add("windowed");

$("openSettings").onclick = () => {
  browser.runtime.sendMessage({ type: "openSettings" });
  if (!document.body.classList.contains("windowed")) window.close();
};
$("pauseAll").onclick = () => act("pauseAll");
$("resumeAll").onclick = () => act("resumeAll");
$("toggleAdd").onclick = () => {
  $("addBox").hidden = !$("addBox").hidden;
  if (!$("addBox").hidden) $("addUrl").focus();
};
$("addBox").addEventListener("submit", (e) => {
  e.preventDefault();
  act("add", { url: $("addUrl").value, pageUrl: $("addPage").value, userAgent: navigator.userAgent }, "Added").then(() => {
    $("addUrl").value = "";
    $("addPage").value = "";
    $("addBox").hidden = true;
  });
});

function makeCard(id) {
  const name = el("span", { class: "name" });
  const status = el("span", { class: "badge" });
  const fill = el("div");
  const sizeText = el("span", { class: "muted num" });
  const speedText = el("span", { class: "num" });
  const err = el("div", { class: "err" });
  const conns = el("div", { class: "conns num" });
  const pause = el("button", { class: "small", onclick: () => act("pause", { id }) }, "Pause");
  const resume = el("button", { class: "small", onclick: () => act("resume", { id }) }, "Resume");
  const folder = el("button", { class: "small", onclick: () => act("showInFolder", { id }) }, "Show in folder");
  const remove = el("button", { class: "small", onclick: () => removeDownload(id, remove) }, "Remove");

  const page = el("input", { type: "url" });
  const sha = el("input", { type: "text", class: "mono", placeholder: "optional" });
  const md5 = el("input", { type: "text", class: "mono", placeholder: "optional" });
  const urlText = el("div", { class: "mono muted", style: "grid-column:1/-1" });
  const details = el("details", {},
    el("summary", {}, "Details"),
    el("div", { class: "form" },
      el("label", {}, "Landing page"), page,
      el("button", { class: "small", onclick: () => act("setPageUrl", { id, pageUrl: page.value }, "Saved") }, "Save"),
      el("label", {}, "Expected SHA-256 / MD5"), sha, el("span"), md5,
      el("button", { class: "small", onclick: () => act("setHashes", { id, sha256: sha.value, md5: md5.value }, "Checksums saved") }, "Save"),
      el("label", {}, "Original link"), urlText));

  const root = el("div", { class: "card" },
    el("div", { class: "top" }, name, status),
    el("div", { class: "bar" }, fill),
    el("div", { class: "meta" }, sizeText, speedText),
    err, conns,
    el("div", { class: "actions" }, pause, resume, folder, remove),
    details);

  const rows = new Map();
  let filled = false;
  let shownPageHint = false;

  function update(d) {
    name.textContent = d.filename || d.url;
    name.title = d.filename || d.url;
    status.textContent = STATUS_TEXT[d.status] || d.status;
    status.className = "badge " + (STATUS_CLASS[d.status] || "");
    fill.style.width = percent(d) + "%";
    sizeText.textContent = d.probed
      ? `${fmtBytes(d.done)} of ${fmtBytes(d.size)} (${percent(d).toFixed(1)}%)${d.rangeOk ? "" : " · main connection only"}`
      : "Checking the link…";
    speedText.textContent = d.status === "running" ? `${fmtSpeed(d.speed)} · ${fmtETA(d.eta)} left` : "";
    err.textContent = d.error || "";
    const running = d.status === "running" || d.status === "verifying" || d.status === "queued";
    pause.hidden = !running;
    resume.hidden = running || d.status === "completed";
    folder.hidden = !d.probed;

    // One line per connection: name, state, connections, speed (and why, if it failed).
    const ids = Object.keys(d.links).sort((a, b) =>
      ifaceName(a, latest.state, latest.settings).localeCompare(ifaceName(b, latest.state, latest.settings)));
    conns.hidden = d.status === "completed" || ids.length === 0;
    for (const ifc of ids) {
      const l = d.links[ifc];
      let r = rows.get(ifc);
      if (!r) {
        r = {
          name: el("span"), state: el("span"), info: el("span", { class: "muted" }), speed: el("span"),
          whyText: el("span"),
          retry: el("button", { class: "small", onclick: () => act("retryLink", { id, iface: ifc }) }, "Retry"),
        };
        r.why = el("div", { class: "why" }, r.whyText, " ", r.retry);
        rows.set(ifc, r);
      }
      conns.append(r.name, r.state, r.info, r.speed, r.why);
      r.name.textContent = ifaceName(ifc, latest.state, latest.settings);
      let label = LINK_TEXT[l.status] || l.status;
      if (l.status === "ready" && d.status === "running" && l.conns > 0) label = "Downloading";
      if (l.needsPage) label = "Needs landing page";
      r.state.replaceChildren(...[
        el("span", { class: "badge " + (l.needsPage ? "warn" : LINK_CLASS[l.status] || "dim") }, label),
        l.resolved ? el("span", { class: "muted" }, " own link") : null,
      ].filter(Boolean));
      r.info.textContent = l.conns ? `${l.conns}×` : "";
      r.speed.textContent = l.speed > 0 ? fmtSpeed(l.speed) : "";
      const canRetry = !l.needsPage && (l.status === "failed" || l.status === "mismatch");
      r.why.hidden = !l.error && !canRetry;
      r.whyText.textContent = l.error || "";
      r.retry.hidden = !canRetry;
    }
    // Point straight at the field that fixes it.
    const needsPage = ids.some((ifc) => d.links[ifc].needsPage);
    if (needsPage && !shownPageHint) {
      details.open = true;
      page.placeholder = "Paste the page you started the download from";
      shownPageHint = true;
    }
    if (!needsPage) shownPageHint = false;

    if (!filled || document.activeElement !== page) page.value = d.pageUrl || "";
    if (!filled) {
      sha.value = d.sha256 || "";
      md5.value = d.md5 || "";
      urlText.textContent = d.url;
      filled = true;
    }
  }
  return { root, update };
}

// Popups can't show confirm dialogs, so removing unfinished data takes a second click.
function removeDownload(id, button) {
  const d = (latest.state.downloads || []).find((x) => x.id === id);
  if (!d) return;
  if (d.status === "completed") return act("remove", { id, deleteFile: false }, "Removed from list (file kept)");
  if (button.dataset.armed) return act("remove", { id, deleteFile: true }, "Removed");
  button.dataset.armed = "1";
  button.textContent = "Delete partial data?";
  button.classList.add("primary");
  setTimeout(() => {
    delete button.dataset.armed;
    button.textContent = "Remove";
    button.classList.remove("primary");
  }, 4000);
}

function renderDownloads(state) {
  const list = $("list");
  const downloads = (state.downloads || []).slice().reverse();
  const seen = new Set();
  for (const d of downloads) {
    seen.add(d.id);
    let c = cards.get(d.id);
    if (!c) {
      c = makeCard(d.id);
      cards.set(d.id, c);
    }
    c.update(d);
    list.append(c.root); // keeps order newest first
  }
  for (const [id, c] of cards) {
    if (!seen.has(id)) {
      c.root.remove();
      cards.delete(id);
    }
  }
  const empty = $("emptyList");
  if (!downloads.length && !empty) list.append(el("div", { class: "empty", id: "emptyList" }, "No downloads yet. Big downloads in Firefox are picked up automatically."));
  if (downloads.length && empty) empty.remove();
}

connectUI((m) => {
  latest = m;
  renderConn(m, "Connected");
  if (!m.connected || !m.state) {
    $("list").replaceChildren(el("div", { class: "offline" },
      "The download engine isn't reachable. Make sure it's running in the tray, or reinstall it (installer\\install.ps1).",
      m.error ? el("div", { class: "muted mono" }, m.error) : null));
    cards.clear();
    return;
  }
  if ($("list").querySelector(".offline")) $("list").replaceChildren();
  const s = m.state;
  $("total").textContent = s.active ? `${fmtSpeed(s.speed)} · ${fmtETA(s.eta)} left` : "idle";
  $("chips").replaceChildren(...s.interfaces.filter((i) => i.present && i.enabled)
    .map((i) => el("span", {}, `${i.name}${i.isMain ? " (main)" : ""}: ${fmtSpeed(i.speed)}`)));
  renderDownloads(s);
});
