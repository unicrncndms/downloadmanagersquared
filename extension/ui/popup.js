"use strict";

// The downloads view: the toolbar popup, or a small window when opened from the tray.

let latest = {};
const cards = new Map(); // download id -> card controller

if (new URLSearchParams(location.search).has("window")) document.body.classList.add("windowed");

$("openSettings").onclick = () => {
  Host.openSettings("");
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
  const add = { url: $("addUrl").value, pageUrl: $("addPage").value, vpn: $("addVpn").value };
  if (Host.userAgent !== null) add.userAgent = Host.userAgent; // else the engine uses the browser's
  act("add", add, "Added").then(() => {
    $("addUrl").value = "";
    $("addPage").value = "";
    $("addBox").hidden = true;
  });
});

function makeCard(id) {
  const name = el("span", { class: "name" });
  const status = el("span", { class: "badge" });
  const vpn = el("span", { class: "badge dim" });
  const fill = el("div");
  const sizeText = el("span", { class: "muted num" });
  const speedText = el("span", { class: "num" });
  // Torrents only: the swarm and the upload, below the progress.
  const peersText = el("span", { class: "muted num" });
  const upText = el("span", { class: "num" });
  const swarm = el("div", { class: "meta" }, peersText, upText);
  const err = el("div", { class: "err" });
  const conns = el("div", { class: "conns num" });
  const pause = el("button", { class: "small", onclick: () => act("pause", { id }) }, "Pause");
  const resume = el("button", { class: "small", onclick: () => act("resume", { id }) }, "Resume");
  const folder = el("button", { class: "small", onclick: () => act("showInFolder", { id }) }, "Show in folder");
  const remove = el("button", { class: "small", onclick: () => removeDownload(id, actions, ask) }, "Remove");
  // Popups can't show confirm dialogs, so removing an unfinished download asks inline.
  const ask = el("div", { class: "actions ask", hidden: true },
    el("span", { class: "muted" }, "Remove this download?"),
    el("button", { class: "small primary", onclick: () => act("remove", { id, deleteFile: true }, "Removed, partial file deleted") }, "Delete partial file"),
    el("button", { class: "small", onclick: () => act("remove", { id, deleteFile: false }, "Removed from list (partial file kept)") }, "Keep partial file"),
    el("button", { class: "small", onclick: () => { ask.hidden = true; actions.hidden = false; } }, "Cancel"));
  const actions = el("div", { class: "actions" }, pause, resume, folder, remove);
  // The add step: a torrent waits here until its files and folder are chosen.
  const pick = el("div", { class: "pick", hidden: true });
  let picking = ""; // what the add step shows: the VPN question and/or the files

  const page = el("input", { type: "url" });
  const sha = el("input", { type: "text", class: "mono", placeholder: "optional" });
  const md5 = el("input", { type: "text", class: "mono", placeholder: "optional" });
  const urlText = el("div", { class: "mono muted", style: "grid-column:1/-1" });
  const httpDetails = el("div", { class: "form" },
    el("label", {}, "Landing page"), page,
    el("button", { class: "small", onclick: () => act("setPageUrl", { id, pageUrl: page.value }, "Saved") }, "Save"),
    el("label", {}, "Expected SHA-256 / MD5"), sha, el("span"), md5,
    el("button", { class: "small", onclick: () => act("setHashes", { id, sha256: sha.value, md5: md5.value }, "Checksums saved") }, "Save"),
    el("label", {}, "Original link"), urlText);
  const facts = el("dl", { class: "facts" });
  const details = el("details", {}, el("summary", {}, "Details"), httpDetails, facts);

  const root = el("div", { class: "card" },
    el("div", { class: "top" }, name, vpn, status),
    el("div", { class: "bar" }, fill),
    el("div", { class: "meta" }, sizeText, speedText),
    swarm,
    err, pick, conns,
    actions, ask,
    details);

  const rows = new Map();
  let filled = false;
  let shownPageHint = false;

  function update(d) {
    const torrent = d.kind === "torrent";
    name.textContent = d.filename || d.url;
    vpn.textContent = VPN_TEXT[d.vpn] || "";
    vpn.hidden = !VPN_TEXT[d.vpn];
    name.title = d.filename || d.url;
    status.textContent = d.label;
    status.className = "badge " + (d.tone || "");
    fill.style.width = percent(d) + "%";
    const choosing = d.status === "choosing";
    const files = d.files || [];
    sizeText.textContent = choosing && files.length
      ? `${files.length} file${files.length === 1 ? "" : "s"} · ${fmtBytes(files.reduce((n, f) => n + f.size, 0))}`
      : progressText(d);
    speedText.textContent = d.status === "running" ? `↓ ${fmtSpeed(d.speed)} · ${fmtETA(d.eta)} left` : "";

    // The swarm: who we're connected to, and what we've given back.
    const inSwarm = !!d.inSwarm;
    swarm.hidden = !torrent || choosing || !(inSwarm || d.uploaded > 0);
    peersText.textContent = !inSwarm ? ""
      : `${plural(d.peers || 0, "peer")} · ${plural(d.seeds || 0, "seed")}${d.swarm > (d.peers || 0) ? ` · ${d.swarm} known` : ""}`;
    upText.textContent = `↑ ${inSwarm ? fmtSpeed(d.upSpeed || 0) + " · " : ""}${fmtBytes(d.uploaded || 0)} sent · ratio ${(d.ratio || 0).toFixed(2)}`;
    err.textContent = d.error || "";
    const can = d.actions || {};
    pause.hidden = !can.pause;
    resume.hidden = !can.resume;
    folder.hidden = !can.showInFolder;
    remove.hidden = !can.remove; // a torrent being added has its own Cancel
    pick.hidden = !choosing;
    const step = choosing ? `${d.askVpn}|${files.length}|${hasVpn()}` : "";
    if (step && step !== picking) renderPick(d);
    picking = step;
    // Landing page and checksums are for HTTP downloads; a torrent shows what it is.
    httpDetails.hidden = torrent;
    facts.hidden = !torrent;
    details.hidden = choosing;
    if (torrent) {
      facts.replaceChildren(...[
        ["Save folder", d.dir],
        ["Info hash", d.infoHash],
        ["VPN", VPN_TEXT[d.vpn] || "Off"],
        ["Magnet link", d.url],
      ].filter(([, v]) => v).flatMap(([k, v]) => [el("dt", {}, k), el("dd", { class: k === "Save folder" || k === "VPN" ? "" : "mono" }, v)]));
    }

    // One line per connection: name, state, connections, speed (and why, if it failed).
    const ids = Object.keys(d.links).sort((a, b) =>
      ifaceName(a, latest.state, latest.settings).localeCompare(ifaceName(b, latest.state, latest.settings)));
    conns.hidden = d.status === "completed" || ids.length === 0;
    let k = 0;
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
      for (const n of [r.name, r.state, r.info, r.speed, r.why]) place(conns, n, k++);
      r.name.textContent = ifaceName(ifc, latest.state, latest.settings);
      r.state.replaceChildren(...[
        el("span", { class: "badge " + (l.tone || "dim") }, l.label),
        l.resolved ? el("span", { class: "muted" }, " own link") : null,
      ].filter(Boolean));
      r.info.textContent = !l.conns ? "" : torrent ? plural(l.conns, "peer") : `${l.conns}×`;
      r.speed.textContent = torrent
        ? [l.speed > 0 ? "↓ " + fmtSpeed(l.speed) : "", l.up > 0 ? "↑ " + fmtSpeed(l.up) : ""].filter(Boolean).join(" ")
        : l.speed > 0 ? fmtSpeed(l.speed) : "";
      r.why.hidden = !l.error && !l.canRetry;
      r.whyText.textContent = l.error || "";
      r.retry.hidden = !l.canRetry;
    }
    // Point straight at the field that fixes it.
    const needsPage = ids.some((ifc) => d.links[ifc].status === "needs-page");
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
  function renderPick(d) {
    const files = d.files || [];
    // Before the torrent first meets peers: suggest a VPN, or stay on this connection.
    const vpnChoice = d.askVpn ? el("div", { class: "vpnask" },
      el("div", {}, el("b", {}, "Join the swarm through a VPN?"), " Every peer sees the address you join from."),
      el("label", {}, el("input", { type: "radio", name: "vpn-" + id, value: "vpn", checked: hasVpn(), disabled: !hasVpn() }),
        hasVpn() ? " Use the VPN (recommended)" : " Use a VPN: none is set up yet ",
        hasVpn() ? null : el("a", { href: "#", onclick: (e) => { e.preventDefault(); Host.openSettings("#pia"); } }, "Set one up")),
      el("label", {}, el("input", { type: "radio", name: "vpn-" + id, value: "off", checked: !hasVpn() }), " Stay on the current connection")) : null;
    const vpnValue = () => (pick.querySelector(`input[name="vpn-${id}"]:checked`) || {}).value;
    if (!files.length) { // a magnet link: the question comes before fetching the files
      pick.replaceChildren(vpnChoice, el("div", { class: "actions" },
        el("button", { class: "small primary", onclick: () => act("chooseVpn", { id, vpn: vpnValue() }) }, "Continue"),
        el("button", { class: "small", onclick: () => act("remove", { id, deleteFile: false }, "Cancelled") }, "Cancel")));
      return;
    }
    const boxes = files.map((f) => el("input", { type: "checkbox", checked: f.selected }));
    const dir = el("input", { type: "text", value: d.dir });
    pick.replaceChildren(vpnChoice,
      el("div", { class: "files" }, files.map((f, i) => el("label", {},
        boxes[i], el("span", { class: "path" }, f.path), el("span", { class: "muted num" }, fmtBytes(f.size))))),
      el("div", { class: "form" },
        el("label", {}, files.length > 1 ? "Save to (in a folder named after the torrent)" : "Save to"), dir, el("span")),
      el("div", { class: "actions" },
        el("button", { class: "small primary", onclick: () => act("chooseFiles", { id, files: boxes.map((b) => b.checked), dir: dir.value, ...(d.askVpn ? { vpn: vpnValue() } : {}) }, "Download queued") }, "Download"),
        el("button", { class: "small", onclick: () => act("remove", { id, deleteFile: false }, "Cancelled") }, "Cancel")));
  }

  return { root, update };
}

// Whether any VPN tunnel is set up, so a torrent can be sent through one.
function hasVpn() {
  return ((latest.state && latest.state.interfaces) || []).some((i) => i.tunnel);
}

function removeDownload(id, actions, ask) {
  const d = (latest.state.downloads || []).find((x) => x.id === id);
  if (!d) return;
  if (!d.partialFile) return act("remove", { id, deleteFile: false }, "Removed from list (files kept)");
  actions.hidden = true;
  ask.hidden = false;
}

function renderDownloads(state) {
  const list = $("list");
  const downloads = (state.downloads || []).slice().reverse();
  const seen = new Set();
  let i = 0;
  for (const d of downloads) {
    seen.add(d.id);
    let c = cards.get(d.id);
    if (!c) {
      c = makeCard(d.id);
      cards.set(d.id, c);
    }
    c.update(d);
    place(list, c.root, i++); // newest first
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

// place puts node at index in parent, moving it only if it isn't there:
// moving an element takes the focus out of a field in it and closes a
// dropdown, and the downloads are redrawn every second.
function place(parent, node, index) {
  const at = parent.children[index];
  if (at !== node) parent.insertBefore(node, at || null);
}

// A line under the header when engine and extension may not work together.
function renderVersion(m) {
  const ext = Host.version;
  const clash = m.connected && versionsClash(m.engineVersion, ext);
  $("versionWarn").hidden = !clash;
  if (clash) {
    $("versionWarn").replaceChildren(`Engine v${m.engineVersion} and extension v${ext} may not work together: `,
      link(RELEASES_URL, "update both"));
  }
}

connectUI((m) => {
  latest = m;
  renderConn(m, "Connected");
  renderVersion(m);
  if (!m.connected || !m.state) {
    $("list").replaceChildren(m.notInstalled
      ? el("div", { class: "offline" },
        el("b", {}, "Engine not installed. "),
        "DMS² needs its download engine on this PC. Download the zip from the ",
        link(RELEASES_URL, "release page"),
        ", extract it and run dms-engine.exe.")
      : el("div", { class: "offline" },
        "The download engine isn't reachable. Make sure it's running in the tray, or run dms-engine.exe from the release zip again.",
        m.error ? el("div", { class: "muted mono" }, m.error) : null));
    cards.clear();
    return;
  }
  if ($("list").querySelector(".offline")) $("list").replaceChildren();
  const s = m.state;
  $("total").textContent = s.active ? `${fmtSpeed(s.speed)} · ${fmtETA(s.eta)} left` : "idle";
  // With torrents in the list, say where peers can connect in.
  const torrents = (s.downloads || []).some((d) => d.kind === "torrent");
  $("chips").replaceChildren(...s.interfaces.filter((i) => i.present && i.enabled)
    .map((i) => el("span", { title: torrents ? INCOMING_TITLE[i.incoming] || "" : "" },
      `${i.name}${i.isMain ? " (main)" : ""}: ${fmtSpeed(i.speed)}${torrents && INCOMING_TEXT[i.incoming] ? " · " + INCOMING_TEXT[i.incoming] : ""}`)));
  renderDownloads(s);
});
