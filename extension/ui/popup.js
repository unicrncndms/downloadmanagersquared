"use strict";

// The downloads view: the toolbar popup, or a small window when opened from the tray.
// A card shows at a glance what a download is doing; the rest is one click
// away, in its "⋯" menu or its Details. Finished downloads shrink to a line.

let latest = {};
const cards = new Map(); // download id -> card controller

if (new URLSearchParams(location.search).has("window")) document.body.classList.add("windowed");

// In the Downloads window the header matches the settings page's.
if (windowHeader("downloads")) document.querySelector("footer").hidden = true;

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
// A .zip link offers Download as folder, ticked as the settings default.
$("addUrl").addEventListener("input", () => {
  const c = addFolderChoice($("addUrl").value, latest.settings);
  if (c.show === !$("addFolderRow").hidden) return; // leave a box the user ticked alone
  $("addFolderRow").hidden = !c.show;
  $("addFolder").checked = c.checked;
});
$("addBox").addEventListener("submit", (e) => {
  e.preventDefault();
  const add = { url: $("addUrl").value, pageUrl: $("addPage").value, vpn: $("addVpn").value };
  if (Host.userAgent !== null) add.userAgent = Host.userAgent; // else the engine uses the browser's
  const folder = addFolderParam(!$("addFolderRow").hidden, $("addFolder").checked, latest.settings);
  if (folder !== undefined) add.folder = folder;
  act("add", add, "Added").then(() => {
    $("addUrl").value = "";
    $("addPage").value = "";
    $("addFolderRow").hidden = true;
    $("addBox").hidden = true;
  });
});

// An open "⋯" menu closes on a click anywhere else.
let openMenu = null;
document.addEventListener("click", (e) => {
  if (openMenu && !openMenu.parentElement.contains(e.target)) closeMenu();
});
function closeMenu() {
  if (openMenu) openMenu.hidden = true;
  openMenu = null;
}

// Small line icons, drawn in the text colour.
const ICONS = {
  pause: "M5 3v10M11 3v10",
  play: "M5 3l8 5-8 5z",
  more: "M3 8h.01M8 8h.01M13 8h.01",
  folder: "M2 4.5h4l1.5 1.5H14v6.5H2z",
  close: "M4 4l8 8M12 4l-8 8",
};
function icon(name) {
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", "0 0 16 16");
  svg.setAttribute("aria-hidden", "true");
  const p = document.createElementNS(ns, "path");
  p.setAttribute("d", ICONS[name]);
  svg.append(p);
  return svg;
}
function iconButton(name, title, onclick) {
  return el("button", { class: "icon", title, "aria-label": title, onclick }, icon(name));
}

function makeCard(id) {
  let d = null; // the download as last shown
  let expanded = false; // a finished download opened back into its card

  const name = el("span", { class: "name", title: "" });
  const status = el("span", { class: "badge" });
  const vpn = el("span", { class: "badge dim" });
  const size = el("span", { class: "muted num size" }); // a finished line's size
  const showDone = iconButton("folder", "Show in folder", () => act("showInFolder", { id }));
  const dropDone = iconButton("close", "Remove from list (files stay)", () => act("remove", { id, deleteFile: false }, "Removed from list"));
  name.addEventListener("click", () => {
    if (!d || !finished(d)) return;
    expanded = !expanded;
    update(d);
  });

  const fill = el("div");
  const bar = el("div", { class: "bar" }, fill);
  const left = el("span", { class: "muted num left" });
  const right = el("span", { class: "num" });
  const pause = iconButton("pause", "Pause", () => act("pause", { id }));
  const resume = iconButton("play", "Resume", () => act("resume", { id }));
  // The "⋯" menu: Show in folder, Copy link, Remove….
  const menuFolder = el("button", { onclick: () => { closeMenu(); act("showInFolder", { id }); } }, "Show in folder");
  const menuCopy = el("button", { onclick: () => { closeMenu(); copyLink(d && d.url); } }, "Copy link");
  const menuRemove = el("button", { onclick: () => { closeMenu(); removeDownload(id, ask); } }, "Remove…");
  const menu = el("div", { class: "menu", hidden: true }, menuFolder, menuCopy, menuRemove);
  const more = iconButton("more", "More", (e) => {
    e.stopPropagation();
    const was = !menu.hidden;
    closeMenu();
    if (!was) { menu.hidden = false; openMenu = menu; }
  });
  const meta = el("div", { class: "meta" }, left, el("span", { class: "end" }, right, pause, resume, el("span", { class: "more" }, more, menu)));
  const sub = el("div", { class: "sub muted num" });
  const via = el("div", { class: "sub muted num" }); // which interfaces, how many connections each

  // Popups can't show confirm dialogs, so removing an unfinished download asks
  // inline; a Download as folder deletes or keeps its folder too.
  let wording = removeWording(null);
  const delButton = el("button", { class: "small primary", onclick: () => act("remove", { id, deleteFile: true }, wording.deleted) });
  const keepButton = el("button", { class: "small", onclick: () => act("remove", { id, deleteFile: false }, wording.kept) });
  const ask = el("div", { class: "actions ask", hidden: true },
    el("span", { class: "muted" }, "Remove this download?"),
    delButton, keepButton,
    el("button", { class: "small", onclick: () => { ask.hidden = true; } }, "Cancel"));

  // Interfaces with a problem, on the card; all of them in Details.
  const cardConns = el("div", { class: "conns num" });
  const allConns = el("div", { class: "conns num" });
  const cardRows = new Map();
  const allRows = new Map();

  // The add step: a torrent waits here until its files and folder are chosen.
  const pick = el("div", { class: "pick", hidden: true });
  let picking = ""; // what the add step shows: the VPN question and/or the files

  // Change…: the recent folders, chosen here, and Other folder… for Windows'
  // picker, which the engine shows and applies itself (the toolbar popup
  // closes as soon as it opens). current is the folder not to offer; chosen
  // gets the folder picked from the list.
  function chooser(current, chosen) {
    const box = el("div", { class: "choices", hidden: true });
    const toggle = () => {
      if (!box.hidden) { box.hidden = true; return; }
      box.replaceChildren(
        ...folderChoices(latest.settings, current()).map((dir) =>
          el("button", { class: "choice", title: dir, onclick: () => { box.hidden = true; chosen(dir); } }, dir)),
        el("button", { class: "choice other", onclick: () => { box.hidden = true; act("pickFolder", { id }); } }, "Other folder…"));
      box.hidden = false;
    };
    return { box, button: el("button", { class: "small", onclick: toggle }, "Change…") };
  }

  // Download as folder switched on by hand: it keeps downloading while this
  // asks where to extract.
  const askInto = el("b", { class: "path" });
  const askChange = chooser(() => askInto.textContent, (dir) => act("setFolder", { id, into: dir }, "Extracting into " + dir));
  const foldAsk = el("div", { class: "extract", hidden: true },
    el("div", { class: "row" },
      el("span", { class: "grow" }, "Extract into ", askInto),
      askChange.button,
      el("button", { class: "small primary", onclick: () => act("setFolder", { id, into: askInto.textContent }, "Extracting into " + askInto.textContent) }, "Start")),
    askChange.box,
    el("a", { href: "#", class: "muted", onclick: (e) => { e.preventDefault(); act("setFolder", { id, on: false }, "Downloading it as a file"); } }, "Download as a file instead"));

  // Details, in sections: Connections, Folder, File, Checks.
  const section = (title, ...body) => el("section", {}, el("h3", {}, title), ...body);
  const connSection = section("Connections", allConns);

  const foldOn = el("input", { type: "checkbox", onchange: () => act("setFolder", { id, on: foldOn.checked }) });
  const foldTag = el("span", { class: "badge dim" }, "unavailable");
  const foldSwitch = el("label", { class: "switch" }, foldOn, "Download as folder: extract it while it downloads", foldTag);
  const foldExtracting = el("div", {}, "Extracting as a folder");
  // Extract into, while unfinished: Change… moves what is extracted (the
  // engine does the move; the state shows how it went).
  const moveInto = el("span", { class: "path grow" });
  const moveWarn = el("div", { class: "muted" });
  const moveErr = el("div", { class: "bad" });
  const moveChange = chooser(() => (d && d.folder && d.folder.into) || "", (dir) => act("setFolder", { id, into: dir }, "Moving to " + dir));
  const foldMove = el("div", {},
    el("div", { class: "row" }, el("span", { class: "muted" }, "Extract into"), moveInto, moveChange.button),
    moveChange.box, moveWarn, moveErr);
  const foldFacts = el("dl", { class: "facts" });
  const foldSection = section("Folder", foldSwitch, foldExtracting, foldMove, foldFacts);

  const fileFacts = el("dl", { class: "facts" });
  const fileSection = section("File", fileFacts);

  // Checks: the landing page and checksums as values, Edit for boxes.
  const checkFacts = el("dl", { class: "facts" });
  const page = el("input", { type: "url" });
  const sha = el("input", { type: "text", class: "mono", placeholder: "64 hex characters (optional)", spellcheck: "false" });
  const md5 = el("input", { type: "text", class: "mono", placeholder: "32 hex characters (optional)", spellcheck: "false" });
  const checkForm = el("div", { class: "form", hidden: true },
    el("label", {}, "Landing page"), page,
    el("label", {}, "SHA-256"), sha,
    el("label", {}, "MD5"), md5,
    el("div", { class: "actions" },
      el("button", { class: "small primary", onclick: saveChecks }, "Save"),
      el("button", { class: "small", onclick: () => editChecks(false) }, "Cancel")));
  const checkEdit = el("button", { class: "small", onclick: () => editChecks(true) }, "Edit");
  const checkSection = section("Checks", checkFacts, checkEdit, checkForm);

  const details = el("details", { ontoggle: () => { if (d) update(d); } },
    el("summary", {}, "Details"), connSection, foldSection, fileSection, checkSection);

  const root = el("div", { class: "card" },
    el("div", { class: "top" }, name, vpn, status, size, showDone, dropDone),
    bar, meta, sub, via,
    cardConns, pick, foldAsk, ask,
    details);

  let shownPageHint = false;

  function editChecks(on) {
    if (on) {
      page.value = d.pageUrl || "";
      sha.value = d.sha256 || "";
      md5.value = d.md5 || "";
    }
    checkForm.hidden = !on;
    checkFacts.hidden = on;
    checkEdit.hidden = on;
  }
  async function saveChecks() {
    await act("setPageUrl", { id, pageUrl: page.value });
    await act("setHashes", { id, sha256: sha.value, md5: md5.value }, "Saved");
    editChecks(false);
  }

  function update(next) {
    d = next;
    const torrent = d.kind === "torrent";
    const done = finished(d);
    const line = done && !expanded;
    root.classList.toggle("line", line);
    name.textContent = d.filename || d.url;
    name.title = done ? (expanded ? "Fold back into a line" : "Open this download") : d.filename || d.url;
    vpn.textContent = VPN_TEXT[d.vpn] || "";
    vpn.hidden = line || !VPN_TEXT[d.vpn];
    status.textContent = d.label;
    status.className = "badge " + (d.tone || "");
    status.hidden = line;
    size.hidden = showDone.hidden = dropDone.hidden = !line;
    size.textContent = d.size > 0 ? fmtBytes(d.size) : "";
    showDone.hidden = !line || !(d.actions || {}).showInFolder;
    if (line) {
      for (const n of [bar, meta, sub, via, cardConns, pick, foldAsk, ask, details]) n.hidden = true;
      return;
    }

    const choosing = d.status === "choosing";
    const seeding = d.status === "seeding";
    const files = d.files || [];
    bar.hidden = seeding;
    fill.style.width = percent(d) + "%";
    const s = statusLine(d);
    meta.hidden = false;
    left.classList.toggle("bad", !!s.error);
    left.classList.toggle("muted", !s.error);
    if (s.error) left.textContent = s.error;
    else if (choosing && files.length) left.textContent = `${plural(files.length, "file")} · ${fmtBytes(files.reduce((n, f) => n + f.size, 0))}`;
    const up = seedLine(d, latest.settings);
    if (!s.error && seeding) left.textContent = up; // the badge already says Seeding
    else if (!s.error && !(choosing && files.length)) left.textContent = s.left;
    right.textContent = seeding ? "" : s.right;
    sub.textContent = s.error ? "" : [!seeding && torrent ? up : "", s.sub].filter(Boolean).join(" · ");
    sub.hidden = !sub.textContent;
    via.textContent = s.error ? "" : viaLine(d, (ifc) => ifaceName(ifc, latest.state, latest.settings));
    via.hidden = !via.textContent;

    const can = d.actions || {};
    pause.hidden = !can.pause;
    resume.hidden = !can.resume;
    menuFolder.hidden = !can.showInFolder;
    menuRemove.hidden = !can.remove; // a torrent being added has its own Cancel
    menuCopy.hidden = !d.url;
    more.parentElement.hidden = choosing;

    pick.hidden = !choosing;
    const step = choosing ? `${d.askVpn}|${files.length}|${hasVpn()}` : "";
    if (step && step !== picking) renderPick(d);
    picking = step;

    // Download as folder: the step while it waits for a folder, and its
    // section in Details.
    const fv = folderView(d, latest.settings);
    foldAsk.hidden = !(fv && fv.ask);
    if (fv && fv.ask) sub.hidden = true; // the engine's "choose a folder" says what the step shows
    if (fv && fv.ask) askInto.textContent = fv.ask.prefill;
    foldSection.hidden = !fv;
    if (fv) {
      foldSwitch.hidden = !fv.toggle;
      if (fv.toggle) {
        foldOn.checked = fv.toggle.on;
        foldOn.disabled = fv.toggle.disabled;
        foldTag.hidden = !fv.toggle.disabled;
        foldTag.title = fv.toggle.reason;
      }
      foldExtracting.hidden = !fv.extracting;
      foldMove.hidden = !fv.move;
      if (fv.move) {
        moveInto.textContent = fv.move.moving ? `${fv.move.into} → ${fv.move.moving}` : fv.move.into;
        moveWarn.textContent = fv.move.warning;
        moveWarn.hidden = !fv.move.warning;
        moveErr.textContent = fv.move.error;
        moveErr.hidden = !fv.move.error;
      }
      foldFacts.hidden = !fv.facts.length;
      foldFacts.replaceChildren(...fv.facts.flatMap(([k, v]) =>
        [el("dt", {}, k), el("dd", { class: k === "Renamed" ? "lines" : "" }, v)]));
    }
    wording = removeWording(d);
    delButton.textContent = wording.del;
    keepButton.textContent = wording.keep;

    // The interfaces: those with a problem on the card, all in Details.
    const ids = Object.keys(d.links || {}).sort((a, b) =>
      ifaceName(a, latest.state, latest.settings).localeCompare(ifaceName(b, latest.state, latest.settings)));
    const problems = new Set(problemLinks(d));
    const shown = ids.filter((ifc) => problems.has(ifc));
    cardConns.hidden = !shown.length;
    renderLinks(cardConns, cardRows, shown);
    connSection.hidden = done || !ids.length;

    details.hidden = choosing;
    if (!details.open) {
      // Point straight at the field that fixes it.
      const needsPage = ids.some((ifc) => d.links[ifc].status === "needs-page");
      if (needsPage && !shownPageHint && !torrent) {
        details.open = true;
        editChecks(true);
        page.placeholder = "Paste the page you started the download from";
        page.focus();
        shownPageHint = true;
      }
      if (!needsPage) shownPageHint = false;
      return; // the rest is only drawn while Details is open
    }
    renderLinks(allConns, allRows, ids);

    const facts = torrent ? [
      ["Save folder", d.dir],
      ["Peers", d.inSwarm ? `${plural(d.peers || 0, "peer")} · ${plural(d.seeds || 0, "seed")}${d.swarm > (d.peers || 0) ? ` · ${d.swarm} known` : ""}` : ""],
      ["Sent", d.uploaded > 0 ? fmtBytes(d.uploaded) : ""],
      ["Info hash", d.infoHash],
      ["VPN", VPN_TEXT[d.vpn] || "Off"],
      ["Magnet link", d.url],
    ] : [
      ["Save folder", d.folder && d.folder.on ? "" : d.dir], // a folder's place is under Folder
      ["Original link", d.url],
    ];
    // A link stays on one line, cut short; all of it shows on hover, and the
    // ⋯ menu copies it.
    fileFacts.replaceChildren(...facts.filter(([, v]) => v).flatMap(([k, v]) =>
      [el("dt", {}, k), el("dd", k.endsWith("link") ? { class: "mono one-line", title: v } : { class: k === "Info hash" ? "mono" : "" }, v)]));
    fileSection.hidden = !fileFacts.children.length;

    checkSection.hidden = torrent;
    if (!torrent && checkForm.hidden) {
      checkFacts.replaceChildren(...[["Landing page", d.pageUrl], ["SHA-256", d.sha256], ["MD5", d.md5]].flatMap(([k, v]) =>
        [el("dt", {}, k), el("dd", !v ? { class: "muted" } : k === "Landing page" ? { class: "mono one-line", title: v } : { class: "mono" }, v || "none")]));
    }
  }

  // One line per interface: name, state, connections, speed (and why, if it failed).
  function renderLinks(grid, rows, ids) {
    const torrent = d.kind === "torrent";
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
      for (const n of [r.name, r.state, r.info, r.speed, r.why]) place(grid, n, k++);
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
    while (grid.children.length > k) grid.lastChild.remove();
    for (const ifc of rows.keys()) if (!ids.includes(ifc)) rows.delete(ifc);
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
        el("label", {}, files.length > 1 ? "Save to (in a folder named after the torrent)" : "Save to"), dir),
      el("div", { class: "actions" },
        el("button", { class: "small primary", onclick: () => act("chooseFiles", { id, files: boxes.map((b) => b.checked), dir: dir.value, ...(d.askVpn ? { vpn: vpnValue() } : {}) }, "Download queued") }, "Download"),
        el("button", { class: "small", onclick: () => act("remove", { id, deleteFile: false }, "Cancelled") }, "Cancel")));
  }

  return { root, update, isLine: () => root.classList.contains("line") };
}

// Whether any VPN tunnel is set up, so a torrent can be sent through one.
function hasVpn() {
  return ((latest.state && latest.state.interfaces) || []).some((i) => i.tunnel);
}

function removeDownload(id, ask) {
  const d = (latest.state.downloads || []).find((x) => x.id === id);
  if (!d) return;
  if (!d.partialFile) return act("remove", { id, deleteFile: false }, "Removed from list (files kept)");
  ask.hidden = false;
}

async function copyLink(url) {
  if (!url) return;
  try {
    await navigator.clipboard.writeText(url);
    toast("Link copied");
  } catch {
    toast("Couldn't copy the link");
  }
}

// The heading over the finished lines, with Clear finished.
const finishedHead = el("div", { class: "group-head" },
  el("span", { class: "muted" }, "Finished"),
  el("a", { href: "#", onclick: (e) => { e.preventDefault(); clearFinished(); } }, "Clear finished"));

async function clearFinished() {
  const done = (latest.state.downloads || []).filter(finished);
  for (const d of done) await call("remove", { id: d.id, deleteFile: false }).catch(() => {});
  toast(done.length === 1 ? "1 finished download cleared (files kept)" : `${done.length} finished downloads cleared (files kept)`);
}

function renderDownloads(state) {
  const list = $("list");
  const downloads = sortDownloads(state.downloads || []);
  const seen = new Set();
  let i = 0;
  let headed = false;
  for (const d of downloads) {
    seen.add(d.id);
    let c = cards.get(d.id);
    if (!c) {
      c = makeCard(d.id);
      cards.set(d.id, c);
    }
    if (finished(d) && !headed) {
      place(list, finishedHead, i++);
      headed = true;
    }
    c.update(d);
    place(list, c.root, i++);
  }
  if (!headed) finishedHead.remove();
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
  renderConn(m);
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
  const bulk = bulkActions(s.downloads || []);
  $("pauseAll").hidden = !bulk.pauseAll;
  $("resumeAll").hidden = !bulk.resumeAll;
  renderDownloads(s);
});
