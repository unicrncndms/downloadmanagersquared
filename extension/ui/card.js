"use strict";

// A download's card in the downloads view (popup.js): a line once finished,
// the full card while at work or opened, and one card for a group.

// makeLine is a finished download before it is ever opened: only its line.
// Its card (makeCard, about a hundred elements) is built when open is called:
// building one for each of thousands of finished downloads took seconds.
function makeLine(id) {
  const name = el("span", { class: "name" });
  const size = el("b", { class: "num" });
  const more = iconButton("chevron", "Details", () => showDetail(id));
  const show = iconButton("folder", "Show in folder", () => act("showInFolder", { id }));
  const drop = iconButton("close", "Remove from list (files stay)", () => act("remove", { id, deleteFile: false }, "Removed from list"));
  const root = el("div", { class: "card line one", tabindex: "-1", "data-id": id },
    el("span", { class: "n" }), name, el("div", { class: "nums" }, size), el("span", { class: "end" }, more, show, drop));
  let sig = "";
  function update(d) {
    root.classList.toggle("sel", selected.has(id));
    const can = !!(d.actions || {}).showInFolder;
    const s = JSON.stringify([d.filename, d.url, d.size, can]);
    if (s === sig) return;
    sig = s;
    name.textContent = name.title = d.filename || d.url;
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
    change.box);
  // Declining the folder or the password: in the ⋯ menu, where it doesn't crowd the answer.
  const asFile = () => set({ on: false }, "Downloading it as a file");
  const passWhy = el("span", { class: "grow" });
  const passIn = el("input", { type: "password", class: "grow", "aria-label": "Archive password", placeholder: "", autocomplete: "off" });
  const pass = el("form", { class: "extract fold pass", hidden: true, onsubmit: (e) => {
    e.preventDefault();
    const password = passIn.value;
    if (!password) return;
    passIn.value = "";
    act("setArchivePassword", { id: getPassId(), password }, "Trying the password");
  } },
    el("div", { class: "row" }, passWhy, passIn, el("button", { class: "small primary" }, "Unlock")));
  // v: a folderView or setFolderView, or null for neither.
  function update(v) {
    pass.hidden = !(v && v.password);
    if (v && v.password) {
      passWhy.textContent = v.password.wrong ? "Wrong password. Try another:" : "Password:";
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
  return { row, pass, ask, update, asFile, declinable: (v) => !!(v && (v.password || v.ask)) };
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

// chunkGrid is the file as squares, from the engine's map of it: "#" on
// disk, "." missing, a digit being fetched right now by connection
// mapBy[digit], which shows in that connection's colour.
function chunkGrid(rows) {
  const root = el("div", { class: "chunks", role: "img", hidden: true });
  root.style.setProperty("--rows", rows);
  let shown = "";
  return {
    root,
    update(d) {
      const map = (d && d.map) || "";
      root.hidden = !map;
      if (!map) return;
      const by = (d.mapBy || []).map((ifc) => ifaceHue(ifc, latest.state));
      const sig = map + by.join();
      if (sig === shown) return;
      shown = sig;
      root.setAttribute("aria-label", `${Math.round(percent(d))}% downloaded`);
      root.style.setProperty("--cols", Math.ceil(map.length / rows));
      while (root.children.length < map.length) root.append(el("i"));
      while (root.children.length > map.length) root.lastChild.remove();
      let cell = root.firstElementChild;
      for (const c of map) {
        const cls = c === "#" ? "on" : c === "." ? "" : "now";
        if (cell.className !== cls) cell.className = cls;
        cell.style.color = cls === "now" ? by[+c] || "" : "";
        cell = cell.nextElementSibling;
      }
    },
  };
}

// A download's row: its name, its file as squares, its two numbers and its
// buttons at a fixed height, then a line saying what it does and through
// which connections. What asks for an answer (a password, a folder, a failed
// link) follows under it. Its Details show beside the list (popup.js detail).
function makeCard(id) {
  let d = null; // the download as last shown

  const num = el("span", { class: "n num" }); // its place in the queue
  const name = el("span", { class: "name", title: "" });
  const status = el("span", { class: "badge st" });
  const vpn = el("span", { class: "badge dim" });
  const showDone = iconButton("folder", "Show in folder", () => act("showInFolder", { id }));
  const dropDone = iconButton("close", "Remove from list (files stay)", () => act("remove", { id, deleteFile: false }, "Removed from list"));

  const grid = chunkGrid(3);
  const fill = el("div");
  const bar = el("div", { class: "bar" }, fill); // where the engine sends no map of the file
  const prog = el("div", { class: "prog" }, grid.root, bar);
  const numsMain = el("b", { class: "num" });
  const numsSub = el("small", { class: "muted num" });
  const nums = el("div", { class: "nums" }, numsMain, numsSub);
  const left = el("span", { class: "num left" }); // an error, or what a torrent uploads or offers
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
  // Record the site's clicks: the site has no recipe yet, so a link that
  // expires later would need someone at the browser. Recording now, while
  // someone is here, lets DMS² renew it alone. Only an offer: nothing records
  // without the click. In the menu, and explained in Details.
  const record = () => act("recordRecipe", { id }, "Click through the page in the window that opens");
  const menuRecord = el("button", { onclick: () => { closeMenu(); record(); } }, "Record the site's clicks…");
  const menuAsFile = el("button", { onclick: () => { closeMenu(); fold.asFile(); } }, "Download as a file instead");
  const menu = el("div", { class: "menu", hidden: true }, menuNext, menuLast, menuFolder, menuCopy, menuRecord, menuAsFile, menuRemove);
  const more = iconButton("more", "More", (e) => {
    e.stopPropagation();
    const was = !menu.hidden;
    closeMenu();
    if (!was) showMenu(menu, e.currentTarget);
  });
  const openDetails = iconButton("chevron", "Details", () => (detail.id === id ? closeDetail() : showDetail(id)));
  const tools = el("span", { class: "end" }, openDetails, moveUp, moveDown, pause, resume, showDone, dropDone, el("span", { class: "more" }, more, menu));
  const sub = el("span", { class: "muted num" });
  const via = el("span", { class: "via muted num" }); // which interfaces, how many connections each
  const mini = el("span", { class: "mini num" }); // the same in a narrow list: colours and counts by the name
  const line = el("div", { class: "what" }, status, vpn, left, via, sub);
  const extract = extractBar();

  // Popups can't show confirm dialogs, so removing an unfinished download asks
  // inline; a Download as folder deletes or keeps its folder too.
  let wording = removeWording(null);
  const delButton = el("button", { class: "small danger", onclick: () => act("remove", { id, deleteFile: true }, wording.deleted) });
  const keepButton = el("button", { class: "small primary", "data-keep": "", onclick: () => act("remove", { id, deleteFile: false }, wording.kept) });
  const askText = el("span", { class: "muted" }, "Remove this download?");
  const ask = el("div", { class: "actions ask", hidden: true },
    askText,
    keepButton, delButton,
    el("button", { class: "small", onclick: () => { ask.hidden = true; } }, "Cancel"));

  // Interfaces that failed, on the card; all of them in Details.
  const cardConns = el("div", { class: "conns num" });
  const allConns = el("div", { class: "conns num" });
  const cardRows = new Map();
  const allRows = new Map();

  // Record recipe: the site has no recipe yet, so a link that expires later
  // would need someone at the browser. Recording now, while someone is here,
  // lets DMS² renew it alone. Only an offer: nothing records without the click.
  const recordSection = el("section", {}, el("h3", {}, "Site"),
    el("p", { class: "muted" }, "If this link expires, DMS² will need you to click through the site again. Recorded once, your clicks are repeated for you."),
    el("button", { class: "small", onclick: record }, "Record the site's clicks"));

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

  // Details: shown in the pane beside the list, or the sheet over it, while
  // this download is the one opened (popup.js detail). A bigger map on top.
  const bigGrid = chunkGrid(4);
  const bigNums = el("div", { class: "muted num" });
  const details = el("details", { ontoggle: () => { activityAt = 0; if (d) update(d); } },
    el("summary", {}, "Details"), bigGrid.root, bigNums, connSection, recordSection, mirrorSection, foldSection, fileSection, checkSection, activitySection);
  function syncDetail() {
    const on = detail.id === id;
    if (on && details.parentNode !== detail.body) {
      detail.body.replaceChildren(details);
      details.open = true;
    }
    if (!on && details.open) {
      details.open = false;
      details.remove();
    }
    if (on) detail.title.textContent = d.filename || d.url;
    root.classList.toggle("detailed", on);
    openDetails.setAttribute("aria-expanded", String(on));
  }

  const extras = [extract.bar, extract.line, cardConns, pick, fold.row, fold.pass, fold.ask, ask];
  const root = el("div", { class: "card", tabindex: "-1", "data-id": id }, num, name, mini, tools, prog, nums, line, ...extras);
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
    dropOn(from, id);
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
    syncDetail();
    root.classList.toggle("sel", selected.has(id));
    // A finished line never changes: with thousands listed, redrawing each every second adds up.
    const sig = done ? JSON.stringify(d) + details.open : "";
    if (done && sig === lineSig) return;
    lineSig = sig;
    const s = statusLine(d);
    // One line: finished, or waiting in the queue with nothing fetched yet.
    const slim = done || (d.status === "queued" && !d.done && !s.error);
    root.classList.toggle("line", done);
    root.classList.toggle("one", slim);
    name.textContent = name.title = d.filename || d.url;
    vpn.textContent = VPN_TEXT[d.vpn] || "";
    vpn.hidden = done || !VPN_TEXT[d.vpn];
    status.textContent = d.label;
    status.className = "badge st " + (d.tone || "");
    // Its section's heading says it, unless it says more (Checking, Extracting) or warns.
    status.hidden = slim || (["Downloading", "Seeding", "Paused", "Queued"].includes(d.label) && !["warn", "bad"].includes(d.tone));
    dropDone.hidden = !done;
    showDone.hidden = !done || !(d.actions || {}).showInFolder;
    const nm = rowNums(d);
    numsMain.textContent = nm.main;
    numsSub.textContent = nm.sub;
    numsSub.hidden = !nm.sub;

    const choosing = d.status === "choosing";
    const seeding = d.status === "seeding";
    const nFiles = d.fileCount || 0; // the list itself comes with the picker
    grid.update(seeding || slim ? null : d);
    bar.hidden = seeding || slim || !grid.root.hidden;
    prog.hidden = grid.root.hidden && bar.hidden;
    root.classList.toggle("idle", d.status !== "running");
    fill.style.width = percent(d) + "%";
    bigGrid.update(details.open && !done ? d : null);
    bigNums.textContent = details.open ? s.left : "";
    const up = seedLine(d, latest.settings);
    left.textContent = s.error ? s.error : choosing && nFiles ? `${plural(nFiles, "file")} · ${fmtBytes(d.filesSize || 0)}` : seeding ? up : "";
    left.classList.toggle("bad", !!s.error);
    left.hidden = !left.textContent;
    // Queued on one line: its number and its section say that it waits.
    sub.textContent = s.error || slim ? "" : [!seeding && torrent ? up : "", s.sub].filter(Boolean).join(" · ");
    sub.hidden = !sub.textContent;
    const used = s.error ? [] : viaLinks(d);
    via.hidden = mini.hidden = !used.length;
    const count = (ifc) => (torrent ? plural(d.links[ifc].conns, "peer") : d.links[ifc].conns + "×");
    const nameOf = (ifc) => ifaceName(ifc, latest.state, latest.settings);
    // A connection held back (the server busy, backing off) says so in its tone; why on hover and in Details.
    const held = (ifc) => (d.links[ifc].tone === "warn" ? d.links[ifc].label : "");
    const viaSig = used.map((ifc) => [nameOf(ifc), count(ifc), held(ifc), ifaceHue(ifc, latest.state)]);
    const entry = (ifc, text) => {
      const dot = el("i", { class: "dot" });
      dot.style.color = ifaceHue(ifc, latest.state);
      return el("span", { class: held(ifc) ? "held" : "", title: [`${nameOf(ifc)}: ${count(ifc)}`, held(ifc)].filter(Boolean).join(" · ") }, dot, text);
    };
    fresh(via, viaSig, () => used.map((ifc) => entry(ifc, `${nameOf(ifc)} ${count(ifc)}`)));
    fresh(mini, viaSig, () => used.map((ifc) => entry(ifc, torrent ? String(d.links[ifc].conns) : count(ifc))));
    line.hidden = done || (slim && left.hidden && vpn.hidden);
    extract.update(extractView([d]));

    const can = d.actions || {};
    pause.hidden = !can.pause;
    resume.hidden = !can.resume;
    menuFolder.hidden = !can.showInFolder;
    menuRemove.hidden = !can.remove; // a torrent being added has its own Cancel
    menuCopy.hidden = !d.url;
    menuRecord.hidden = recordSection.hidden = !can.recordRecipe;
    menuNext.hidden = menuLast.hidden = !queued();
    const all = (latest.state && latest.state.downloads) || [];
    // At an end of the queue an arrow keeps its place, unseen, so pause and ⋯ line up from row to row.
    moveUp.hidden = moveDown.hidden = !queued();
    moveUp.style.visibility = queued() && moveBefore(all, id, "up") === null ? "hidden" : "";
    moveDown.style.visibility = queued() && moveBefore(all, id, "down") === null ? "hidden" : "";
    limitRow.hidden = torrent;
    if (document.activeElement !== limitIn) limitIn.value = d.limit ? +(d.limit / 1e6).toFixed(3) : "";
    root.draggable = queued();
    more.parentElement.hidden = choosing || done;
    openDetails.hidden = choosing;

    pick.hidden = !choosing;
    const step = choosing ? `${d.askVpn}|${nFiles}|${hasVpn()}` : "";
    const newStep = step && step !== picking;
    picking = step;
    if (newStep) renderPick(d);

    // Download as folder: its switch and the step while it waits for a
    // folder on the card, the rest in its section in Details.
    const fv = folderView(d, latest.settings);
    fold.update(fv);
    menuAsFile.hidden = !fold.declinable(fv);
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
    askText.textContent = d.done > 0 ? `Remove this download? ${fmtBytes(d.done)} is downloaded.` : "Remove this download?";
    delButton.textContent = wording.del;
    keepButton.textContent = wording.keep;

    // The interfaces: those with a problem on the card, all in Details.
    const ids = Object.keys(d.links || {}).sort((a, b) =>
      ifaceName(a, latest.state, latest.settings).localeCompare(ifaceName(b, latest.state, latest.settings)));
    // Held back shows on the connection's colour; failed gets a row with why, and Retry.
    const problems = new Set(problemLinks(d));
    const shown = ids.filter((ifc) => problems.has(ifc) && d.links[ifc].tone !== "warn");
    cardConns.hidden = !shown.length;
    renderLinks(cardConns, cardRows, shown);
    connSection.hidden = done || !ids.length;

    if (done) for (const n of extras) n.hidden = true;
    if (!details.open) {
      // Point straight at the field that fixes it, unless Details shows another download.
      const needsPage = ids.some((ifc) => d.links[ifc].status === "needs-page");
      if (needsPage && !shownPageHint && !torrent && !detail.id) {
        showDetail(id, true);
        syncDetail();
        editChecks(true);
        // A fresh link to the file works here too: the browser fetches it like a page.
        page.placeholder = "The page you started from, or a fresh link to the file";
        page.focus();
        shownPageHint = true;
      }
      if (!needsPage) shownPageHint = false;
      if (!details.open) return; // the rest is only drawn while Details is open
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
          name: el("span", { class: "who" }), state: el("span"), info: el("span", { class: "muted" }), speed: el("span"),
          whyText: el("span"),
          retry: el("button", { class: "small", onclick: () => act("retryLink", { id, iface: ifc }) }, "Retry"),
        };
        r.why = el("div", { class: "why" }, r.whyText, " ", r.retry);
        rows.set(ifc, r);
      }
      for (const n of [r.name, r.state, r.info, r.speed, r.why]) place(grid, n, k++);
      fresh(r.name, [ifc, ifaceName(ifc, latest.state, latest.settings), ifaceHue(ifc, latest.state)], () => {
        const dot = el("i", { class: "dot" });
        dot.style.color = ifaceHue(ifc, latest.state);
        return [dot, ifaceName(ifc, latest.state, latest.settings)];
      });
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

  return { root, update, number(n) { num.textContent = n || ""; } };
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

  const num = el("span", { class: "n num" });
  const title = el("span");
  const count = el("span", { class: "badge dim" });
  const name = el("span", { class: "name" }, title, " ", count);
  const status = el("span", { class: "badge st" });
  const fill = el("div");
  const bar = el("div", { class: "bar" }, fill);
  const left = el("span", { class: "muted num left" });
  const numsMain = el("b", { class: "num" });
  const pause = iconButton("pause", "Pause all files", () => each("pause", {}, "", (d) => d.actions && d.actions.pause));
  const resume = iconButton("play", "Resume all files", () => each("resume", {}, "", (d) => d.actions && d.actions.resume));
  const ask = el("div", { class: "actions ask", hidden: true },
    el("span", { class: "muted" }, "Remove all these files' downloads?"),
    el("button", { class: "small primary", "data-keep": "", onclick: () => each("remove", { deleteFile: false }, "Removed from list (files kept)") }, "Remove, keep files"),
    el("button", { class: "small danger", onclick: () => each("remove", { deleteFile: true }, "Removed, unfinished files deleted") }, "Remove and delete unfinished files"),
    el("button", { class: "small", onclick: () => { ask.hidden = true; } }, "Cancel"));
  const menuRemove = el("button", { onclick: () => {
    closeMenu();
    if (groupView(g.items).partialFile) { ask.hidden = false; ask.querySelector("[data-keep]").focus(); }
    else each("remove", { deleteFile: false }, "Removed from list (files kept)");
  } }, "Remove all…");
  const menuAsFile = el("button", { onclick: () => { closeMenu(); fold.asFile(); } }, "Download as a file instead");
  const menu = el("div", { class: "menu", hidden: true }, menuAsFile, menuRemove);
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
  const toggle = iconButton("chevron", "Show its files", () => { open = !open; update(g); });
  const files = el("div", { class: "group-files" });
  const root = el("div", { class: "card group", tabindex: "-1", "data-group": gid },
    num, name, el("span", { class: "end" }, toggle, pause, resume, el("span", { class: "more" }, more, menu)),
    el("div", { class: "prog" }, bar), el("div", { class: "nums" }, numsMain), el("div", { class: "what" }, status, left),
    extract.bar, extract.line, fold.row, fold.pass, fold.ask, ask, files);

  function update(next) {
    g = next;
    const v = groupView(g.items);
    title.textContent = name.title = g.name;
    toggle.setAttribute("aria-expanded", String(open));
    root.classList.toggle("sel", g.items.every((d) => selected.has(d.id)));
    const parts = String(g.group).startsWith("parts:");
    count.textContent = `${g.items.length} ${parts ? "parts" : "files"}`;
    root.classList.toggle("parts", parts);
    const fv = parts ? setFolderView(g.items, latest.settings) : null;
    foldId = fv ? fv.id : "";
    passId = fv && fv.password ? fv.password.id : "";
    extract.update(parts ? extractView(g.items) : null);
    fold.update(fv);
    menuAsFile.hidden = !fold.declinable(fv);
    status.textContent = v.label;
    status.className = "badge st " + v.tone;
    fill.style.width = v.percent + "%";
    left.textContent = v.left;
    numsMain.textContent = v.speed > 0 ? fmtSpeed(v.speed) : "";
    pause.hidden = !v.pause;
    resume.hidden = !v.resume;
    files.hidden = !open;
    let i = 0;
    for (const d of g.items) {
      const c = cardFor(d.id);
      c.update(d);
      place(files, c.root, i++);
    }
    while (files.children.length > i) files.lastChild.remove();
  }

  return { root, update, ids, number(n) { num.textContent = n || ""; } };
}

function cardFor(id) {
  let c = cards.get(id);
  if (!c) {
    let line = null, full = null, d = null;
    // The whole row in place of the finished line: it has the Details.
    const whole = () => {
      if (full) return;
      full = makeCard(id);
      if (line) line.root.replaceWith(full.root);
      line = null;
      if (d) full.update(d);
    };
    c = {
      get root() { return (full || line).root; },
      whole,
      number(n) { if (full) full.number(n); },
      update(next) {
        d = next;
        if (!full && !finished(d)) whole(); // back at work (a checksum being checked), or never finished
        else if (!full && !line) line = makeLine(id);
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
  ask.querySelector("[data-keep]").focus(); // keeping is the default; deleting takes a deliberate click
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
