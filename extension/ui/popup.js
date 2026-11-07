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
const zipLine = (url) => addFolderChoice(url, latest.settings).show;
$("addUrl").addEventListener("input", () => {
  const c = addFolderChoice("", latest.settings);
  const show = addLines($("addUrl").value).urls.some((u) => zipLine(u.url));
  if (show === !$("addFolderRow").hidden) return; // leave a box the user ticked alone
  $("addFolderRow").hidden = !show;
  $("addFolder").checked = c.checked;
});
// Several links, one per line: each is added as if alone. Lines that fail
// stay in the box, so they can be fixed and added again.
$("addBox").addEventListener("submit", async (e) => {
  e.preventDefault();
  const { urls, bad } = addLines($("addUrl").value);
  const many = urls.length + bad.length > 1;
  const failed = bad.map((b) => ({ ...b, error: "not a link" }));
  for (const { line, url } of urls) {
    const add = { url, pageUrl: $("addPage").value, vpn: $("addVpn").value };
    if (Host.userAgent !== null) add.userAgent = Host.userAgent; // else the engine uses the browser's
    const folder = addFolderParam(!$("addFolderRow").hidden && zipLine(url), $("addFolder").checked, latest.settings);
    if (folder !== undefined) add.folder = folder;
    try {
      await call("add", add);
    } catch (err) {
      failed.push({ line, url, error: err.message });
    }
  }
  const added = urls.length + bad.length - failed.length;
  if (failed.length) {
    failed.sort((a, b) => a.line - b.line);
    $("addUrl").value = failed.map((f) => f.url).join("\n");
    const why = failed.map((f) => (many ? `Line ${f.line}: ${f.error}` : f.error)).join(" · ");
    toast(added ? `${plural(added, "download")} added. ${why}` : why);
    return;
  }
  toast(added === 1 ? "Added" : `${added} downloads added`);
  $("addUrl").value = "";
  $("addPage").value = "";
  $("addFolderRow").hidden = true;
  $("addBox").hidden = true;
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
  // A queued download moves with these, or by dragging it onto another queued one.
  const menuNext = el("button", { onclick: () => { closeMenu(); moveDownload(id, "first"); } }, "Start next");
  const menuLast = el("button", { onclick: () => { closeMenu(); moveDownload(id, "last"); } }, "Start last");
  const menu = el("div", { class: "menu", hidden: true }, menuNext, menuLast, menuFolder, menuCopy, menuRemove);
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

  // Record recipe: the site has no recipe yet, so a link that expires later
  // would need someone at the browser. Recording now, while someone is here,
  // lets DMS² renew it alone. Only an offer: nothing records without the click.
  const recordRow = el("div", { class: "extract", hidden: true },
    el("div", { class: "row" },
      el("span", { class: "grow muted" }, "If this link expires, DMS² will need you to click through the site again."),
      el("button", { class: "small primary", onclick: () => act("recordRecipe", { id }, "Click through the page in the window that opens") }, "Record recipe")));

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
  // An HTTP download's own speed limit, on top of the connections' and the global one.
  const limitIn = el("input", { type: "number", min: 0, step: "any", placeholder: "none", id: "limit-" + id,
    onchange: () => act("setLimit", { id, limit: Math.round((Number(limitIn.value) || 0) * 1e6) }, "Saved") });
  const limitRow = el("div", { class: "row" }, el("label", { for: "limit-" + id, class: "muted" }, "Speed limit (MB/s)"), limitIn);
  const connSection = section("Connections", allConns, limitRow);

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
  // Mirrors: each source of the file, who uses it, and why one was dropped.
  const mirrorList = el("div", { class: "conns mirrors num" });
  const mirrorSection = section("Mirrors", mirrorList);

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
    el("summary", {}, "Details"), connSection, mirrorSection, foldSection, fileSection, checkSection);

  const root = el("div", { class: "card" },
    el("div", { class: "top" }, name, vpn, status, size, showDone, dropDone),
    bar, meta, sub, via,
    cardConns, recordRow, pick, foldAsk, ask,
    details);
  const queued = () => d && d.status === "queued";
  root.addEventListener("dragstart", (e) => {
    e.dataTransfer.setData(DRAG_TYPE, id);
    e.dataTransfer.effectAllowed = "move";
  });
  root.addEventListener("dragover", (e) => {
    if (!queued() || !e.dataTransfer.types.includes(DRAG_TYPE)) return;
    e.preventDefault();
    root.classList.add("drop");
  });
  root.addEventListener("dragleave", () => root.classList.remove("drop"));
  root.addEventListener("drop", (e) => {
    root.classList.remove("drop");
    const from = e.dataTransfer.getData(DRAG_TYPE);
    if (!from || !queued()) return;
    e.preventDefault();
    moveDownload(from, id);
  });

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
      for (const n of [bar, meta, sub, via, cardConns, recordRow, pick, foldAsk, ask, details]) n.hidden = true;
      return;
    }

    const choosing = d.status === "choosing";
    const seeding = d.status === "seeding";
    const nFiles = d.fileCount || 0; // the list itself comes with the picker
    bar.hidden = seeding;
    fill.style.width = percent(d) + "%";
    const s = statusLine(d);
    meta.hidden = false;
    left.classList.toggle("bad", !!s.error);
    left.classList.toggle("muted", !s.error);
    if (s.error) left.textContent = s.error;
    else if (choosing && nFiles) left.textContent = `${plural(nFiles, "file")} · ${fmtBytes(d.filesSize || 0)}`;
    const up = seedLine(d, latest.settings);
    if (!s.error && seeding) left.textContent = up; // the badge already says Seeding
    else if (!s.error && !(choosing && nFiles)) left.textContent = s.left;
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
    recordRow.hidden = !can.recordRecipe;
    menuNext.hidden = menuLast.hidden = !queued();
    limitRow.hidden = torrent;
    if (document.activeElement !== limitIn) limitIn.value = d.limit ? +(d.limit / 1e6).toFixed(3) : "";
    root.draggable = queued();
    more.parentElement.hidden = choosing;

    pick.hidden = !choosing;
    const step = choosing ? `${d.askVpn}|${nFiles}|${hasVpn()}` : "";
    const newStep = step && step !== picking;
    picking = step;
    if (newStep) renderPick(d);

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

    const mirrors = mirrorRows(d, (ifc) => ifaceName(ifc, latest.state, latest.settings));
    mirrorSection.hidden = !mirrors.length;
    mirrorList.replaceChildren(...mirrors.flatMap((m) => [
      el("span", { class: "mono one-line", title: m.url }, m.host),
      m.dropped ? el("span", { class: "badge bad" }, "Dropped") : el("span", { class: "muted" }, m.users.join(", ") || "unused"),
      m.dropped ? el("div", { class: "why" }, m.dropped) : null,
    ].filter(Boolean)));

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

  // The state carries only how many files a torrent has, so a big one stays
  // within what Firefox passes on: the list is asked for here.
  async function renderPick(d) {
    let files = [];
    if (d.fileCount) {
      const step = picking;
      try { files = await call("torrentFiles", { id }); } catch (e) { return toast(e.message); }
      if (picking !== step) return; // a newer render took over meanwhile
    }
    // Before the torrent first meets peers: suggest a VPN, or stay on this connection.
    const vpnChoice = d.askVpn ? el("div", { class: "vpnask" },
      el("div", {}, el("b", {}, "Join the swarm through a VPN?"), " Every peer sees the address you join from."),
      el("label", {}, el("input", { type: "radio", name: "vpn-" + id, value: "vpn", checked: hasVpn(), disabled: !hasVpn() }),
        hasVpn() ? " Use the VPN (recommended)" : " Use a VPN: none is set up yet ",
        hasVpn() ? null : el("a", { href: "#", onclick: (e) => { e.preventDefault(); Host.openSettings("#pia"); } }, "Set one up")),
      el("label", {}, el("input", { type: "radio", name: "vpn-" + id, value: "off", checked: !hasVpn() }), " Stay on the current connection")) : ""; // not null: replaceChildren would print "null"
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

// A Metalink's files as one card: their progress added up, pause, resume
// and remove for all of them, and the files' own cards inside.
function makeGroupCard(gid) {
  let g = null; // the group as last shown
  let open = false;
  const ids = () => (g ? g.items.map((d) => d.id) : []);
  const each = async (method, params, okText, pick) => {
    for (const d of g.items) if (!pick || pick(d)) await call(method, { id: d.id, ...params }).catch((e) => toast(e.message));
    if (okText) toast(okText);
  };

  const name = el("span", { class: "name expandable" });
  const count = el("span", { class: "badge dim" });
  const status = el("span", { class: "badge" });
  const fill = el("div");
  const bar = el("div", { class: "bar" }, fill);
  const left = el("span", { class: "muted num left" });
  const right = el("span", { class: "num" });
  const pause = iconButton("pause", "Pause all files", () => each("pause", {}, "", (d) => d.actions && d.actions.pause));
  const resume = iconButton("play", "Resume all files", () => each("resume", {}, "", (d) => d.actions && d.actions.resume));
  const ask = el("div", { class: "actions ask", hidden: true },
    el("span", { class: "muted" }, "Remove all these files' downloads?"),
    el("button", { class: "small primary", onclick: () => each("remove", { deleteFile: true }, "Removed, unfinished files deleted") }, "Remove and delete unfinished files"),
    el("button", { class: "small", onclick: () => each("remove", { deleteFile: false }, "Removed from list (files kept)") }, "Remove, keep files"),
    el("button", { class: "small", onclick: () => { ask.hidden = true; } }, "Cancel"));
  const menuRemove = el("button", { onclick: () => {
    closeMenu();
    if (groupView(g.items).partialFile) ask.hidden = false;
    else each("remove", { deleteFile: false }, "Removed from list (files kept)");
  } }, "Remove all…");
  const menu = el("div", { class: "menu", hidden: true }, menuRemove);
  const more = iconButton("more", "More", (e) => {
    e.stopPropagation();
    const was = !menu.hidden;
    closeMenu();
    if (!was) { menu.hidden = false; openMenu = menu; }
  });
  const toggle = el("a", { href: "#", class: "muted", onclick: (e) => { e.preventDefault(); open = !open; update(g); } });
  const files = el("div", { class: "group-files" });
  const root = el("div", { class: "card group" },
    el("div", { class: "top" }, name, count, status),
    bar, el("div", { class: "meta" }, left, el("span", { class: "end" }, right, pause, resume, el("span", { class: "more" }, more, menu))),
    ask, toggle, files);
  name.addEventListener("click", () => { open = !open; update(g); });

  function update(next) {
    g = next;
    const v = groupView(g.items);
    name.textContent = g.name;
    name.title = open ? "Hide the files" : "Show the files";
    count.textContent = `${g.items.length} files`;
    status.textContent = v.label;
    status.className = "badge " + v.tone;
    fill.style.width = v.percent + "%";
    left.textContent = v.left;
    right.textContent = v.right;
    pause.hidden = !v.pause;
    resume.hidden = !v.resume;
    toggle.textContent = open ? "Hide files" : "Show files";
    files.hidden = !open;
    let i = 0;
    for (const d of g.items) {
      const c = cardFor(d.id);
      c.update(d);
      place(files, c.root, i++);
    }
    while (files.children.length > i) files.lastChild.remove();
  }

  return { root, update, ids };
}

function cardFor(id) {
  let c = cards.get(id);
  if (!c) {
    c = makeCard(id);
    cards.set(id, c);
  }
  return c;
}

// Whether any VPN tunnel is set up, so a torrent can be sent through one.
function hasVpn() {
  return ((latest.state && latest.state.interfaces) || []).some((i) => i.tunnel);
}

const DRAG_TYPE = "application/x-dms-download";

// moveDownload moves queued download id onto target: another queued download's
// id, or "first" or "last".
function moveDownload(id, target) {
  const before = moveBefore(latest.state.downloads || [], id, target);
  if (before !== null) act("move", { id, before });
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

const groupCards = new Map(); // Metalink group id -> card controller

function renderDownloads(state) {
  const list = $("list");
  const downloads = sortDownloads(state.downloads || []);
  const seen = new Set();
  const seenGroups = new Set();
  let i = 0;
  let headed = false;
  for (const e of listEntries(downloads)) {
    const done = e.d ? finished(e.d) : e.items.every(finished);
    if (done && !headed) {
      place(list, finishedHead, i++);
      headed = true;
    }
    if (e.d) {
      seen.add(e.d.id);
      const c = cardFor(e.d.id);
      c.update(e.d);
      place(list, c.root, i++);
      continue;
    }
    seenGroups.add(e.group);
    for (const d of e.items) seen.add(d.id);
    let g = groupCards.get(e.group);
    if (!g) {
      g = makeGroupCard(e.group);
      groupCards.set(e.group, g);
    }
    g.update(e);
    place(list, g.root, i++);
  }
  if (!headed) finishedHead.remove();
  for (const [id, c] of cards) {
    if (!seen.has(id)) {
      c.root.remove();
      cards.delete(id);
    }
  }
  for (const [id, g] of groupCards) {
    if (!seenGroups.has(id)) {
      g.root.remove();
      groupCards.delete(id);
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
    groupCards.clear();
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
