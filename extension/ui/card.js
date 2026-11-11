"use strict";

// A download's card in the downloads view (popup.js): a line once finished,
// the full card while at work or opened, and one card for a group.

// makeLine is a finished download before it is ever opened: only its line.
// Its card (makeCard, about a hundred elements) is built when open is called:
// building one for each of thousands of finished downloads took seconds.
function makeLine(id, open) {
  const name = el("span", { class: "name", title: "Open this download", onclick: open });
  pressable(name, true, false);
  const size = el("span", { class: "muted num size" });
  const show = iconButton("folder", "Show in folder", () => act("showInFolder", { id }));
  const drop = iconButton("close", "Remove from list (files stay)", () => act("remove", { id, deleteFile: false }, "Removed from list"));
  const root = el("div", { class: "card line" }, el("div", { class: "top" }, name, size, show, drop));
  let sig = "";
  function update(d) {
    const can = !!(d.actions || {}).showInFolder;
    const s = JSON.stringify([d.filename, d.url, d.size, can]);
    if (s === sig) return;
    sig = s;
    name.textContent = d.filename || d.url;
    size.textContent = d.size > 0 ? fmtBytes(d.size) : "";
    show.hidden = !can;
  }
  return { root, update };
}

// folderChooser is Change…: the recent folders, chosen here, and Other folder…
// for Windows' picker, which the engine shows and applies itself (the toolbar
// popup closes as soon as it opens). current is the folder not to offer;
// chosen gets the folder picked from the list.
function folderChooser(getId, current, chosen) {
  const box = el("div", { class: "choices", hidden: true });
  const toggle = () => {
    if (!box.hidden) { box.hidden = true; return; }
    box.replaceChildren(
      ...folderChoices(latest.settings, current()).map((dir) =>
        el("button", { class: "choice", title: dir, onclick: () => { box.hidden = true; chosen(dir); } }, dir)),
      el("button", { class: "choice other", onclick: () => { box.hidden = true; act("pickFolder", { id: getId() }); } }, "Other folder…"));
    box.hidden = false;
  };
  return { box, button: el("button", { class: "small", onclick: toggle }, "Change…") };
}

// folderSwitch is Download as folder on a card: the switch (row), once
// switched on by hand the step asking where to extract (ask), and for an
// encrypted archive the form asking its password (pass): it keeps
// downloading meanwhile. A split archive's group card has one for all its
// parts: the engine sets every part from any one of them.
function folderSwitch(getId, getPassId) {
  const set = (params, okText) => act("setFolder", { id: getId(), ...params }, okText);
  const on = el("input", { type: "checkbox", onchange: () => set({ on: on.checked }) });
  const tag = el("span", { class: "badge dim" }, "unavailable");
  const row = el("label", { class: "switch fold", hidden: true }, on, "Download as folder: extract it while it downloads", tag);
  const into = el("b", { class: "path" });
  const change = folderChooser(getId, () => into.textContent, (dir) => set({ into: dir }, "Extracting into " + dir));
  const ask = el("div", { class: "extract fold", hidden: true },
    el("div", { class: "row" },
      el("span", { class: "grow" }, "Extract into ", into),
      change.button,
      el("button", { class: "small primary", onclick: () => set({ into: into.textContent }, "Extracting into " + into.textContent) }, "Start")),
    change.box,
    el("a", { href: "#", class: "muted", onclick: (e) => { e.preventDefault(); set({ on: false }, "Downloading it as a file"); } }, "Download as a file instead"));
  const passWhy = el("span", { class: "grow" });
  const passIn = el("input", { type: "password", class: "grow", "aria-label": "Archive password", placeholder: "Password", autocomplete: "off" });
  const pass = el("form", { class: "extract fold", hidden: true, onsubmit: (e) => {
    e.preventDefault();
    const password = passIn.value;
    if (!password) return;
    passIn.value = "";
    act("setArchivePassword", { id: getPassId(), password }, "Trying the password");
  } },
    el("div", { class: "row" }, passWhy),
    el("div", { class: "row" }, passIn, el("button", { class: "small primary" }, "Unlock")),
    el("a", { href: "#", class: "muted", onclick: (e) => { e.preventDefault(); set({ on: false }, "Downloading it as a file"); } }, "Download as a file instead"));
  // v: a folderView or setFolderView, or null for neither.
  function update(v) {
    pass.hidden = !(v && v.password);
    if (v && v.password) {
      passWhy.textContent = v.password.wrong ? "That password didn't open it. Try another:" : "It's protected with a password:";
      passWhy.classList.toggle("bad", v.password.wrong);
    }
    row.hidden = !(v && v.toggle);
    if (v && v.toggle) {
      on.checked = v.toggle.on;
      on.disabled = v.toggle.disabled;
      tag.hidden = !v.toggle.disabled;
      tag.title = v.toggle.reason;
    }
    ask.hidden = !(v && v.ask);
    if (v && v.ask) into.textContent = v.ask.prefill;
  }
  return { row, pass, ask, update };
}

// extractBar is a Download as folder's extraction under the download's own
// bar: how far it is, and what it is doing or waiting for (extractView).
function extractBar() {
  const fill = el("div");
  const bar = el("div", { class: "bar xbar fold", hidden: true }, fill);
  const line = el("div", { class: "sub muted num fold", hidden: true });
  return {
    bar, line,
    update(v) {
      bar.hidden = line.hidden = !v;
      if (!v) return;
      fill.style.width = v.percent + "%";
      line.textContent = v.text;
    },
  };
}

// expanded: a finished download opened back into its card.
function makeCard(id, expanded = false) {
  let d = null; // the download as last shown

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
  // A queued download moves one place with these, or by dragging it onto another queued one.
  const moveUp = iconButton("up", "Start earlier", () => moveDownload(id, "up"));
  const moveDown = iconButton("down", "Start later", () => moveDownload(id, "down"));
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
    if (!was) showMenu(menu, e.currentTarget);
  });
  const meta = el("div", { class: "meta" }, left, el("span", { class: "end" }, right, moveUp, moveDown, pause, resume, el("span", { class: "more" }, more, menu)));
  const sub = el("div", { class: "sub muted num" });
  const via = el("div", { class: "sub muted num" }); // which interfaces, how many connections each
  const extract = extractBar();

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

  // Download as folder: its switch, and where to extract once switched on by hand.
  const fold = folderSwitch(() => id, () => id);
  const chooser = (current, chosen) => folderChooser(() => id, current, chosen);

  // Details, in sections: Connections, Folder, File, Checks.
  const section = (title, ...body) => el("section", {}, el("h3", {}, title), ...body);
  // An HTTP download's own speed limit, on top of the connections' and the global one.
  const limitIn = el("input", { type: "number", min: 0, step: "any", placeholder: "none", id: "limit-" + id,
    onchange: () => act("setLimit", { id, limit: Math.round((Number(limitIn.value) || 0) * 1e6) }, "Saved") });
  const limitRow = el("div", { class: "row" }, el("label", { for: "limit-" + id, class: "muted" }, "Speed limit (MB/s)"), limitIn);
  const connSection = section("Connections", allConns, limitRow);

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
  const foldSection = section("Folder", foldExtracting, foldMove, foldFacts);

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

  // Activity: the engine's log of this download, newest first, asked for
  // while Details is open (it stays out of the state sent every second).
  const activityList = el("ol", { class: "activity num" });
  let activity = [];
  let activityAt = 0;
  const activityCopy = el("button", { class: "small", onclick: async () => {
    try {
      await navigator.clipboard.writeText(activity.join("\n"));
      toast("Activity copied");
    } catch {
      toast("Couldn't copy the activity");
    }
  } }, "Copy");
  const activitySection = section("Activity", activityList, activityCopy);
  activitySection.hidden = true; // until the log comes
  async function loadActivity() {
    if (Date.now() - activityAt < 2000) return;
    activityAt = Date.now();
    let events = [];
    try { events = await call("downloadLog", { id }); } catch { return; }
    activity = logLines(events, (ifc) => ifaceName(ifc, latest.state, latest.settings));
    activityList.replaceChildren(...activity.map((line) => el("li", {}, line)));
    activitySection.hidden = !activity.length;
  }

  const details = el("details", { ontoggle: () => { activityAt = 0; if (d) update(d); } },
    el("summary", {}, "Details"), connSection, mirrorSection, foldSection, fileSection, checkSection, activitySection);

  const root = el("div", { class: "card" },
    el("div", { class: "top" }, name, vpn, status, size, showDone, dropDone),
    bar, meta, sub, via, extract.bar, extract.line,
    cardConns, recordRow, pick, fold.row, fold.pass, fold.ask, ask,
    details);
  const queued = () => d && d.status === "queued";
  const takes = () => d && (d.status === "queued" || d.status === "running"); // a drop: its place, or its slot
  root.addEventListener("dragstart", (e) => {
    e.dataTransfer.setData(DRAG_TYPE, id);
    e.dataTransfer.effectAllowed = "move";
  });
  root.addEventListener("dragover", (e) => {
    if (!takes() || !e.dataTransfer.types.includes(DRAG_TYPE)) return;
    e.preventDefault();
    root.classList.add("drop");
  });
  root.addEventListener("dragleave", () => root.classList.remove("drop"));
  root.addEventListener("drop", (e) => {
    root.classList.remove("drop");
    const from = e.dataTransfer.getData(DRAG_TYPE);
    if (!from || !takes()) return;
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

  let lineSig = ""; // the finished line as last drawn
  function update(next) {
    d = next;
    const torrent = d.kind === "torrent";
    const done = finished(d);
    const line = done && !expanded;
    // A finished line never changes: with thousands listed, redrawing each every second adds up.
    const sig = line ? JSON.stringify(d) : "";
    if (line && sig === lineSig) return;
    lineSig = sig;
    root.classList.toggle("line", line);
    name.textContent = d.filename || d.url;
    name.title = done ? (expanded ? "Fold back into a line" : "Open this download") : d.filename || d.url;
    pressable(name, done, expanded);
    vpn.textContent = VPN_TEXT[d.vpn] || "";
    vpn.hidden = line || !VPN_TEXT[d.vpn];
    status.textContent = d.label;
    status.className = "badge " + (d.tone || "");
    status.hidden = line;
    size.hidden = showDone.hidden = dropDone.hidden = !line;
    size.textContent = d.size > 0 ? fmtBytes(d.size) : "";
    showDone.hidden = !line || !(d.actions || {}).showInFolder;
    if (line) {
      for (const n of [bar, meta, sub, via, extract.bar, extract.line, cardConns, recordRow, pick, fold.row, fold.pass, fold.ask, ask, details]) n.hidden = true;
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
    extract.update(extractView([d]));

    const can = d.actions || {};
    pause.hidden = !can.pause;
    resume.hidden = !can.resume;
    menuFolder.hidden = !can.showInFolder;
    menuRemove.hidden = !can.remove; // a torrent being added has its own Cancel
    menuCopy.hidden = !d.url;
    recordRow.hidden = !can.recordRecipe;
    menuNext.hidden = menuLast.hidden = !queued();
    const all = (latest.state && latest.state.downloads) || [];
    moveUp.hidden = !queued() || moveBefore(all, id, "up") === null;
    moveDown.hidden = !queued() || moveBefore(all, id, "down") === null;
    limitRow.hidden = torrent;
    if (document.activeElement !== limitIn) limitIn.value = d.limit ? +(d.limit / 1e6).toFixed(3) : "";
    root.draggable = queued();
    more.parentElement.hidden = choosing;

    pick.hidden = !choosing;
    const step = choosing ? `${d.askVpn}|${nFiles}|${hasVpn()}` : "";
    const newStep = step && step !== picking;
    picking = step;
    if (newStep) renderPick(d);

    // Download as folder: its switch and the step while it waits for a
    // folder on the card, the rest in its section in Details.
    const fv = folderView(d, latest.settings);
    fold.update(fv);
    if (fv && fv.ask) sub.hidden = true; // the engine's "choose a folder" says what the step shows
    if (fv && fv.password) sub.hidden = true; // the engine's "needs the archive's password" says what the form asks
    foldSection.hidden = !fv || !(fv.extracting || fv.move || fv.facts.length);
    if (fv) {
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
      fresh(foldFacts, fv.facts, () => fv.facts.flatMap(([k, v]) =>
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
    cardConns.hidden = !shown.length || details.open; // open, Details lists them under Connections
    renderLinks(cardConns, cardRows, shown);
    connSection.hidden = done || !ids.length;

    details.hidden = choosing;
    if (!details.open) {
      // Point straight at the field that fixes it.
      const needsPage = ids.some((ifc) => d.links[ifc].status === "needs-page");
      if (needsPage && !shownPageHint && !torrent) {
        details.open = true;
        editChecks(true);
        // A fresh link to the file works here too: the browser fetches it like a page.
        page.placeholder = "The page you started from, or a fresh link to the file";
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
    fresh(fileFacts, facts, () => facts.filter(([, v]) => v).flatMap(([k, v]) =>
      [el("dt", {}, k), el("dd", k.endsWith("link") ? { class: "mono one-line", title: v } : { class: k === "Info hash" ? "mono" : "" }, v)]));
    fileSection.hidden = !fileFacts.children.length;

    const mirrors = mirrorRows(d, (ifc) => ifaceName(ifc, latest.state, latest.settings));
    mirrorSection.hidden = !mirrors.length;
    fresh(mirrorList, mirrors, () => mirrors.flatMap((m) => [
      el("span", { class: "mono one-line", title: m.url }, m.host),
      m.dropped ? el("span", { class: "badge bad" }, "Dropped") : el("span", { class: "muted" }, m.users.join(", ") || "unused"),
      m.dropped ? el("div", { class: "why" }, m.dropped) : null,
    ].filter(Boolean)));

    if (torrent) activitySection.hidden = true;
    else loadActivity();

    checkSection.hidden = torrent;
    if (!torrent && checkForm.hidden) {
      const checks = [["Landing page", d.pageUrl], ["SHA-256", d.sha256], ["MD5", d.md5]];
      fresh(checkFacts, checks, () => checks.flatMap(([k, v]) =>
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
      fresh(r.state, [l.tone, l.label, l.resolved, l.family], () => [
        el("span", { class: "badge " + (l.tone || "dim") }, l.label),
        l.resolved ? el("span", { class: "muted" }, " own link") : null,
        l.family ? el("span", { class: "muted", title: "The address family this connection fetches the file over" }, " " + l.family) : null,
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

  return { root, update, name };
}

// A Metalink's files, or a split archive's parts, as one card: their progress added up, pause, resume
// and remove for all of them, and the files' own cards inside.
function makeGroupCard(gid) {
  let g = null; // the group as last shown
  let open = false;
  const ids = () => (g ? g.items.map((d) => d.id) : []);
  const each = async (method, params, okText, pick) => {
    // All at once: a pause answers only once its connections have closed.
    await Promise.all(g.items.filter((d) => !pick || pick(d)).map((d) => call(method, { id: d.id, ...params }).catch((e) => toast(e.message))));
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
    if (!was) showMenu(menu, e.currentTarget);
  });
  // A split archive is one Download as folder: its switch is here, not on its parts' cards.
  let foldId = "", passId = "";
  const fold = folderSwitch(() => foldId, () => passId);
  const extract = extractBar();
  const toggle = el("a", { href: "#", class: "muted", onclick: (e) => { e.preventDefault(); open = !open; update(g); } });
  const files = el("div", { class: "group-files" });
  const root = el("div", { class: "card group" },
    el("div", { class: "top" }, name, count, status),
    bar, el("div", { class: "meta" }, left, el("span", { class: "end" }, right, pause, resume, el("span", { class: "more" }, more, menu))),
    extract.bar, extract.line, fold.row, fold.pass, fold.ask, ask, toggle, files);
  name.addEventListener("click", () => { open = !open; update(g); });

  function update(next) {
    g = next;
    const v = groupView(g.items);
    name.textContent = g.name;
    name.title = open ? "Hide the files" : "Show the files";
    pressable(name, true, open);
    const parts = String(g.group).startsWith("parts:");
    count.textContent = `${g.items.length} ${parts ? "parts" : "files"}`;
    root.classList.toggle("parts", parts);
    const fv = parts ? setFolderView(g.items, latest.settings) : null;
    foldId = fv ? fv.id : "";
    passId = fv && fv.password ? fv.password.id : "";
    extract.update(parts ? extractView(g.items) : null);
    fold.update(fv);
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
    let line = null, full = null, d = null;
    const open = () => {
      full = makeCard(id, true);
      line.root.replaceWith(full.root);
      line = null;
      full.update(d);
      full.name.focus(); // the name pressed is gone: the keyboard stays on its successor
    };
    c = {
      get root() { return (full || line).root; },
      update(next) {
        d = next;
        if (!full && !finished(d)) { // back at work (a checksum being checked), or never finished
          full = makeCard(id);
          if (line) line.root.replaceWith(full.root);
          line = null;
        }
        if (!full && !line) line = makeLine(id, open);
        (full || line).update(d);
      },
    };
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
