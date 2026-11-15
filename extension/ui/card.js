"use strict";

// A download's card in the downloads view (popup.js): a line once finished,
// the full card while at work or opened, and one card for a group.

// makeLine is a finished download before it is ever opened: only its line.
// Its card (makeCard, about a hundred elements) is built when open is called:
// building one for each of thousands of finished downloads took seconds.
// doneActions are a finished download's menu items beyond Show in folder and
// Copy link: Open, Download again, Delete file… (which asks inline first, as
// popups can't show confirm dialogs) and that question. sync shows them as the
// engine's actions allow.
function doneActions(id) {
  const open = menuItem("Open", () => act("open", { id }));
  const again = menuItem("Download again", () => act("again", { id }, "Download started"));
  const del = menuItem("Delete file…", () => { ask.hidden = false; ask.querySelector(".danger").focus(); });
  const ask = confirmRow("Delete this file? It goes to the Recycle Bin.",
    el("button", { class: "small danger", onclick: () => act("deleteFile", { id }, "Moved to the Recycle Bin") }, "Delete file"));
  const sync = (d, on) => {
    const can = d.actions || {};
    open.hidden = !on || !can.open;
    again.hidden = !on || !can.again;
    del.hidden = !on || !can.deleteFile;
    if (!on || !can.deleteFile) ask.hidden = true;
  };
  return { open, again, del, ask, sync };
}

function makeLine(id) {
  const name = el("span", { class: "name" });
  const size = el("b", { class: "num" });
  const more = iconButton("chevron", "Details", () => showDetail(id));
  const show = iconButton("folder", "Show in folder", () => act("showInFolder", { id }));
  const drop = iconButton("close", "Remove from list (files stay)", () => removeWithUndo([id]));
  // The ⋯ menu is built when first used: thousands of lines stay cheap.
  let menu = null, fin = null, cur = null;
  const dots = menuButton(() => buildMenu());
  const host = el("span", { class: "more" }, dots);
  function buildMenu() {
    if (menu) return menu;
    fin = doneActions(id);
    const { folder, copy } = fileItems(id, () => cur && cur.url);
    menu = el("div", { class: "menu", hidden: true }, fin.open, folder, copy, fin.again, fin.del);
    menu.sync = (d) => { fin.sync(d, true); folder.hidden = !(d.actions || {}).showInFolder; copy.hidden = !d.url; };
    host.append(menu);
    root.append(fin.ask);
    if (cur) menu.sync(cur);
    return menu;
  }
  const root = el("div", { class: "card line one", tabindex: "-1", "data-id": id },
    el("span", { class: "n st-tile done", role: "img", "aria-label": "Finished", title: "Finished" }, icon("check")), name,
    el("div", { class: "nums" }, size), el("span", { class: "end" }, more, show, drop, host));
  root.buildMenu = buildMenu; // a right-click builds it too (menu.js)
  let sig = "";
  function update(d) {
    cur = d;
    root.classList.toggle("sel", selected.has(id));
    // What the line shows, cheaply: a finished download hardly ever changes, and its summary costs more than this.
    const can = d.actions || {};
    const links = d.links || {};
    const s = [d.filename, d.url, d.size, d.active, can.showInFolder, can.open, can.again, can.deleteFile,
      ...Object.keys(links).map((i) => i + links[i].bytes + ifaceName(i, latest.state, latest.settings))].join("\n");
    if (s === sig) return;
    sig = s;
    const summary = finishedSummary(d, (i) => ifaceName(i, latest.state, latest.settings));
    name.textContent = name.title = d.filename || d.url;
    size.textContent = d.size > 0 ? fmtBytes(d.size) : "";
    size.title = summary;
    show.hidden = !(d.actions || {}).showInFolder;
    if (menu) menu.sync(d);
  }
  return { root, update };
}

// folderChooser is Change…: the recent folders, chosen here, and Other folder…
// for Windows' picker, which the engine shows and applies itself (the toolbar
// popup closes as soon as it opens). current is the folder not to offer;
// chosen gets the folder picked from the list. other is what Other folder…
// does (the engine's picker for the download getId() by default); null: it is
// not offered.
function folderChooser(getId, current, chosen, other) {
  const box = el("div", { class: "choices", hidden: true });
  const toggle = () => {
    if (!box.hidden) { box.hidden = true; return; }
    box.replaceChildren(
      ...folderChoices(latest.settings, current()).map((dir) =>
        el("button", { class: "choice", type: "button", title: dir, onclick: () => { box.hidden = true; chosen(dir); } }, dir)),
      ...(other === null ? [] : [el("button", { class: "choice other", type: "button", onclick: () => { box.hidden = true; (other || (() => act("pickFolder", { id: getId() })))(); } }, "Other folder…")]));
    box.hidden = false;
  };
  return { box, button: el("button", { class: "small", type: "button", onclick: toggle }, "Change…") };
}

// folderControl is the save folder as the Add box and a torrent's add step
// ask for it: the folder (the download folder until another is chosen: get()
// is "" then, meaning no override) and Change…. Other folder… needs the
// answer back in the page, which only the Downloads window outlives (the
// toolbar popup closes behind the picker), so the popup offers the recent
// folders alone.
function folderControl(start) {
  const saveDir = () => (latest.settings && latest.settings.saveDir) || "";
  let dir = sameFolder(start, saveDir()) ? "" : start || "";
  const path = el("b", { class: "path" });
  const paint = () => { path.textContent = dir || saveDir(); };
  const pick = (d) => { dir = sameFolder(d, saveDir()) ? "" : d; paint(); };
  const windowed = document.body.classList.contains("windowed");
  const change = folderChooser(null, () => dir || saveDir(), pick, windowed ? async () => {
    try {
      const r = await call("browseFolder", { start: dir || saveDir() });
      if (r && r.dir) pick(r.dir);
    } catch (e) { toast(e.message, true); }
  } : null);
  paint();
  const root = el("div", {}, el("div", { class: "row" }, el("span", { class: "muted" }, "Save to"), path, change.button), change.box);
  return { root, get: () => dir, reset() { dir = ""; paint(); }, paint };
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

// keepSelect is a Download as folder's archive keeping (lib/folder.js
// keepView): what it does with the archive, set for the download or its
// whole Set until any of it is freed, with a line on what it means.
function keepSelect(getId) {
  const sel = el("select", { "aria-label": "Archive", onchange: () => act("setFolder", { id: getId(), keep: sel.value }, "Saved") });
  const help = el("div", { class: "muted" });
  const root = el("div", { class: "extract fold keep", hidden: true },
    el("div", { class: "row" }, el("span", { class: "muted" }, "Archive"), sel), help);
  function update(k) {
    root.hidden = !k;
    if (!k) return;
    if (!sel.options.length) sel.replaceChildren(...k.options.map(([v, text]) => el("option", { value: v }, text)));
    if (document.activeElement !== sel) sel.value = k.value;
    sel.disabled = k.disabled;
    sel.title = k.reason;
    help.textContent = k.decided ? `${k.decided}. ${k.help}` : k.help;
  }
  return { root, update };
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

// paintTile shows a status tile (lib/list.js tileOf): its tone and icon, or a
// queued download's place in the queue. label is what it says to a screen
// reader and on hover.
function paintTile(tile, t, place, label) {
  const cls = "n st-tile " + t.kind;
  const sig = [cls, t.icon, place, label].join("|");
  if (tile.dataset.sig === sig) return;
  tile.dataset.sig = sig;
  tile.className = cls;
  tile.title = label;
  tile.setAttribute("aria-label", label);
  tile.replaceChildren(t.icon ? icon(t.icon) : String(place || ""));
}

// chunkGrid is the file as squares, from the engine's map of it: "." missing,
// a letter on disk and written by connection mapBy[a=0, b=1…], a digit being
// fetched right now by mapBy[digit], each in that connection's colour; "#" on
// disk from before this run, in the accent. A square only partly written shows
// how much (d.mapPart, see partTiles in lib/uirules.js). The file shows the sum.
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
      const part = partTiles(d.mapPart, map.length);
      const sig = map + (d.mapFill || "") + (d.mapPart || "") + by.join();
      if (sig === shown) return;
      shown = sig;
      root.setAttribute("aria-label", `${Math.round(percent(d))}% downloaded`);
      root.style.setProperty("--cols", Math.ceil(map.length / rows));
      while (root.children.length < map.length) root.append(el("i"));
      while (root.children.length > map.length) root.lastChild.remove();
      let cell = root.firstElementChild, i = 0;
      for (const c of map) {
        const got = c >= "a" && c <= "j";
        const p = part[i];
        const cls = c === "#" ? "on" : c === "." ? (p ? "part" : "") : got ? "got" : "now";
        if (cell.className !== cls) cell.className = cls;
        const color = cls === "now" ? by[+c] || "" : got ? by[c.charCodeAt(0) - 97] || "" : cls === "part" ? (p.who >= 0 && by[p.who]) || "var(--accent)" : "";
        if (cell.dataset.color !== color) { cell.dataset.color = color; cell.style.color = color; }
        if (cls === "part") cell.style.setProperty("--p", p.n / 9);
        if (cls === "now") cell.style.setProperty("--f", frontFill((d.mapFill || "")[i], p, !!d.mapPart));
        cell = cell.nextElementSibling;
        i++;
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

  const num = el("span", { class: "n st-tile", role: "img" }); // what it does now, or its place in the queue
  const grip = el("span", { class: "grip", title: "Drag onto another queued download to move it", hidden: true }, icon("grip"));
  let queuePlace = 0;
  const name = el("span", { class: "name", title: "" });
  const status = el("span", { class: "badge st" });
  const vpn = el("span", { class: "badge dim" });
  const showDone = iconButton("folder", "Show in folder", () => act("showInFolder", { id }));
  const dropDone = iconButton("close", "Remove from list (files stay)", () => removeWithUndo([id]));

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
  const moveUp = iconButton("up", "Move up", () => moveDownload(id, "up"));
  const moveDown = iconButton("down", "Move down", () => moveDownload(id, "down"));
  // The "⋯" menu: Show in folder, Copy link, Remove….
  const { folder: menuFolder, copy: menuCopy } = fileItems(id, () => d && d.url);
  const menuRemove = menuItem("Remove…", () => removeDownload(id, ask));
  // A queued download moves with these, or by dragging it onto another queued one.
  const menuNext = menuItem("Move to top", () => moveDownload(id, "first"));
  const menuLast = menuItem("Move to bottom", () => moveDownload(id, "last"));
  // Starts at once, whatever Downloads at once says.
  const menuNow = menuItem("Start now", () => act("startNow", { id }));
  // Record the site's clicks: the site has no recipe yet, so a link that
  // expires later would need someone at the browser. Recording now, while
  // someone is here, lets DMS² renew it alone. Only an offer: nothing records
  // without the click. In the menu, and explained in Details.
  const record = () => act("recordRecipe", { id }, "Click through the page in the window that opens");
  const menuRecord = menuItem("Record the site's clicks…", record);
  const menuAsFile = menuItem("Download as a file instead", () => fold.asFile());
  const fin = doneActions(id);
  const menu = el("div", { class: "menu", hidden: true }, menuNow, menuNext, menuLast, fin.open, menuFolder, menuCopy, fin.again, menuRecord, menuAsFile, menuRemove, fin.del);
  const more = menuButton(() => menu);
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
  const ask = confirmRow("Remove this download?", keepButton, delButton);

  // Interfaces that failed, on the card; all of them in Details.
  const cardConns = el("div", { class: "conns num" });
  const allConns = el("div", { class: "conns num" });
  const cardRows = new Map();
  const allRows = new Map();

  // Record recipe: the site has no recipe yet, so a link that expires later
  // would need someone at the browser. Recording now, while someone is here,
  // lets DMS² renew it alone. Only an offer: nothing records without the click.
  const recordSection = el("section", {}, el("h3", { class: "mk" }, "Site"),
    el("p", { class: "muted" }, "If this link expires, DMS² will need you to click through the site again. Recorded once, your clicks are repeated for you."),
    el("button", { class: "small", onclick: record }, "Record the site's clicks"));

  // The add step: a torrent waits here until its files and folder are chosen.
  const picker = pickStep(id);

  // Download as folder: its switch, and where to extract once switched on by hand.
  const fold = folderSwitch(() => id, () => id);
  const chooser = (current, chosen, other) => folderChooser(() => id, current, chosen, other);

  // Details, in sections: Connections, Folder, File, Checks.
  const section = detailSection;
  // An HTTP download's own speed limit, on top of the connections' and the global one.
  const limitIn = el("input", { type: "number", min: 0, step: "any", placeholder: "none", id: "limit-" + id,
    onchange: () => act("setLimit", { id, limit: speedFromUnit(Number(limitIn.value) || 0) }, "Saved") });
  const limitLabel = el("label", { for: "limit-" + id, class: "muted" });
  const limitUnit = el("span", { class: "muted" }); // the unit in the field itself
  const limitRow = el("div", { class: "row" }, limitLabel, limitIn, limitUnit);
  // Each connection's share of the speed, as a bar of its colour.
  const share = el("div", { class: "share", "aria-hidden": "true" });
  const legend = el("p", { class: "muted" });
  const connSection = section("Connections", share, allConns, legend, limitRow);

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
  const foldKeep = keepSelect(() => id);
  const foldFacts = el("dl", { class: "facts" });
  const foldSection = section("Folder", foldExtracting, foldMove, foldKeep.root, foldFacts);

  const fileFacts = el("dl", { class: "facts" });
  // Save folder of an unfinished HTTP download that is paused or queued: Change…
  // moves the partial file (the engine does the move). A running one asks to be
  // paused first, as it has the file open.
  const dirPath = el("span", { class: "path grow" });
  const dirChange = chooser(() => (d && d.dir) || "", (dir) => act("setDir", { id, dir }, "Moving to " + dir), () => act("pickDir", { id }));
  const dirMove = el("div", { hidden: true },
    el("div", { class: "row" }, el("span", { class: "muted" }, "Save folder"), dirPath, dirChange.button),
    dirChange.box);
  const tfiles = torrentFilesPart(id, () => d);
  const summaryLine = el("p", { class: "muted num", style: "margin: 0 0 var(--s2)" });
  const fileSection = section("File", summaryLine, dirMove, fileFacts);
  // Mirrors: each source of the file, who uses it, and why one was dropped.
  const mirrorList = el("div", { class: "conns mirrors num" });
  const mirrorSection = section("Mirrors", mirrorList);

  const srcSum = sourceSumPart(id, () => d);
  const activity = activityPart(id);

  // Details: shown in the pane beside the list, or the sheet over it, while
  // this download is the one opened (popup.js detail). A bigger map on top.
  const bigGrid = chunkGrid(3);
  const bigSpeed = el("b");
  const bigLeft = el("span", { class: "muted" });
  const bigNums = el("div", { class: "read num" }, bigSpeed, bigLeft);
  const details = el("details", { ontoggle: () => { activity.reset(); if (d) update(d); } },
    el("summary", {}, "Details"), bigGrid.root, bigNums, connSection, recordSection, mirrorSection, foldSection, tfiles.section, fileSection, srcSum.sourceSection, srcSum.checkSection, activity.section);
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

  // A link that needs its landing page: a button on the row; Details opens only when it is clicked.
  const needsPageBtn = el("button", { class: "small", hidden: true, onclick: () => {
    showDetail(id);
    srcSum.editSource(true);
    srcSum.page.focus();
  } }, "Needs the page");
  // An SFTP server's key the engine hasn't met: nothing was sent to it; Trust keeps the key, Remove cancels.
  const trustText = el("span", { class: "grow" });
  const trust = el("div", { class: "extract fold", hidden: true }, el("div", { class: "row" }, trustText,
    el("button", { class: "small primary", onclick: () => act("trustHostKey", { id }, "Key trusted") }, "Trust")));
  const extras = [extract.bar, extract.line, cardConns, needsPageBtn, trust, picker.root, fold.row, fold.pass, fold.ask, ask];
  // The name and its connections share the first line: the connections never squeeze the squares.
  const root = el("div", { class: "card", tabindex: "-1", "data-id": id }, num, el("span", { class: "title" }, grip, name, mini), tools, prog, nums, line, ...extras, fin.ask);
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
    status.hidden = slim || !!d.plain;
    dropDone.hidden = !done;
    showDone.hidden = !done || !(d.actions || {}).showInFolder;
    paintTile(num, tileOf(d), queuePlace, d.label);
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
    bigSpeed.textContent = details.open ? nm.main : "";
    bigLeft.textContent = details.open ? [s.left, d.status === "running" ? `${fmtETA(d.eta)} left` : ""].filter(Boolean).join(" · ") : "";
    const up = seedLine(d, latest.settings);
    const errIfc = s.error ? errorSource(d) : "";
    const leftText = s.error ? s.error : choosing && nFiles ? `${plural(nFiles, "file")} · ${fmtBytes(d.filesSize || 0)}` : seeding ? up : "";
    const leftSig = [leftText, errIfc && ifaceName(errIfc, latest.state, latest.settings), errIfc && ifaceHue(errIfc, latest.state)].join("|");
    if (left.dataset.sig !== leftSig) {
      left.dataset.sig = leftSig;
      if (errIfc) { // the error leads with its connection's dot and name
        const dot = el("i", { class: "dot" });
        dot.style.color = ifaceHue(errIfc, latest.state);
        left.replaceChildren(dot, ifaceName(errIfc, latest.state, latest.settings) + ": " + leftText);
      } else left.textContent = leftText;
    }
    left.classList.toggle("bad", !!s.error);
    left.hidden = !left.textContent;
    // Queued on one line: its number and its section say that it waits.
    sub.textContent = s.error || slim ? "" : [!seeding && torrent ? up : "", s.sub].filter(Boolean).join(" · ");
    sub.hidden = !sub.textContent;
    const used = s.error ? [] : viaLinks(d, ((latest.state && latest.state.interfaces) || []).map((x) => x.id));
    via.hidden = mini.hidden = !used.length;
    const count = (ifc) => (torrent ? plural(d.links[ifc].conns, "peer") : plural(d.links[ifc].conns, "request"));
    const nameOf = (ifc) => ifaceName(ifc, latest.state, latest.settings);
    // A connection held back (the server busy, backing off) says "busy" in words; why on hover and in Details.
    const held = (ifc) => (d.links[ifc].tone === "warn" ? d.links[ifc].label : "");
    const viaSig = used.map((ifc) => [nameOf(ifc), count(ifc), held(ifc), ifaceHue(ifc, latest.state)]);
    const says = (ifc) => [`${nameOf(ifc)}: ${count(ifc)}`, held(ifc)].filter(Boolean).join(" · ");
    const entry = (ifc, text) => {
      const dot = el("i", { class: "dot" });
      dot.style.color = ifaceHue(ifc, latest.state);
      return el("span", { class: held(ifc) ? "held" : "", title: says(ifc) }, dot, text, held(ifc) ? el("b", {}, " · busy") : null);
    };
    fresh(via, viaSig, () => used.map((ifc) => entry(ifc, `${nameOf(ifc)} ${count(ifc)}`)));
    // By the name: each connection's tile with how many it has open; its name on hover and to a screen reader.
    fresh(mini, viaSig, () => used.map((ifc) => {
      const tile = el("span", { class: "ctile", role: "img", "aria-label": says(ifc), title: says(ifc) }, el("span", {}, String(d.links[ifc].conns)));
      tile.style.color = ifaceHue(ifc, latest.state);
      if (held(ifc)) tile.classList.add("busy"); // a ring instead of a fill
      return el("span", {}, tile);
    }));
    line.hidden = done || (slim && left.hidden && vpn.hidden);
    extract.update(extractView([d]));

    const can = d.actions || {};
    pause.hidden = !can.pause;
    pause.title = pause.ariaLabel = seeding ? "Stop seeding" : "Pause";
    resume.hidden = !can.resume;
    menuFolder.hidden = !can.showInFolder;
    menuRemove.hidden = !can.remove; // a torrent being added has its own Cancel
    menuRemove.textContent = d.partialFile ? "Remove…" : "Remove"; // the dots only where a question follows
    grip.hidden = !queued();
    menuCopy.hidden = !d.url;
    menuRecord.hidden = recordSection.hidden = !can.recordRecipe;
    menuNow.hidden = menuNext.hidden = menuLast.hidden = !queued();
    // At an end of the queue an arrow keeps its place, unseen, so pause and ⋯ line up from row to row.
    moveUp.hidden = moveDown.hidden = !queued();
    const at = queue.at.get(id); // the page's count of the queue (popup.js), not a scan of every download
    moveUp.style.visibility = queued() && at === 0 ? "hidden" : "";
    moveDown.style.visibility = queued() && at === queue.len - 1 ? "hidden" : "";
    limitRow.hidden = torrent;
    legend.textContent = `The number on a connection's tile is how many ${torrent ? "peers" : "requests"} it has open. A ring around it means the server is holding it back.`;
    limitLabel.textContent = `Speed limit (${speedUnitLabel()})`;
    limitUnit.textContent = speedUnitLabel();
    if (document.activeElement !== limitIn) limitIn.value = unitField(d.limit);
    root.draggable = queued();
    more.parentElement.hidden = choosing;
    fin.sync(d, done);
    openDetails.hidden = choosing;

    picker.update(d, choosing);

    // Download as folder: its switch and the step while it waits for a
    // folder on the card, the rest in its section in Details.
    const fv = folderView(d, latest.settings);
    fold.update(fv);
    menuAsFile.hidden = !fold.declinable(fv);
    if (fv && fv.ask) sub.hidden = true; // the engine's "choose a folder" says what the step shows
    if (fv && fv.password) sub.hidden = true; // the engine's "needs the archive's password" says what the form asks
    foldSection.hidden = !fv || !(fv.extracting || fv.move || fv.keep || fv.facts.length);
    foldKeep.update(fv && fv.keep);
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
    ask.text.textContent = d.done > 0 ? `Remove this download? ${fmtBytes(d.done)} is downloaded.` : "Remove this download?";
    delButton.textContent = wording.del;
    keepButton.textContent = wording.keep;

    // The interfaces: those with a problem on the card, all in Details.
    const ids = Object.keys(d.links || {}).sort((a, b) =>
      ifaceName(a, latest.state, latest.settings).localeCompare(ifaceName(b, latest.state, latest.settings)));
    // Held back shows on the connection's colour; failed gets a row with why, and Retry.
    const problems = new Set(problemLinks(d));
    const shown = ids.filter((ifc) => problems.has(ifc) && d.links[ifc].tone !== "warn");
    cardConns.hidden = !shown.length;
    renderLinks(id, d, cardConns, cardRows, shown);
    connSection.hidden = done || !ids.length;

    if (done) for (const n of extras) n.hidden = true;
    trust.hidden = done || !d.hostKey;
    if (d.hostKey) trustText.textContent = `Trust ${d.hostKey.host}? Its key is ${d.hostKey.fingerprint}. Nothing, not even your login, has been sent to it.`;
    // A button on the row, never a sheet opening by itself: the user may be typing in the search.
    needsPageBtn.hidden = done || torrent || !ids.some((ifc) => d.links[ifc].status === "needs-page");
    if (!details.open) return; // the rest is only drawn while Details is open
    renderLinks(id, d, allConns, allRows, ids);
    const fast = ids.filter((ifc) => d.links[ifc].speed > 0);
    fresh(share, fast.map((ifc) => [ifc, Math.round(d.links[ifc].speed / 1e5), ifaceHue(ifc, latest.state)]), () => fast.map((ifc) => {
      const part = el("i");
      part.style.flexGrow = d.links[ifc].speed;
      part.style.color = ifaceHue(ifc, latest.state);
      return part;
    }));
    share.hidden = fast.length < 2; // one connection has all of it: nothing to compare

    const movable = canMoveFolder(d);
    dirMove.hidden = !movable;
    dirPath.textContent = d.dir || "";
    dirPath.title = d.dir || "";
    const facts = torrent ? [
      ["Save folder", d.dir],
      ["Peers", d.inSwarm ? `${plural(d.peers || 0, "peer")} · ${plural(d.seeds || 0, "seed")}${d.swarm > (d.peers || 0) ? ` · ${d.swarm} known` : ""}` : ""],
      ["Sent", d.uploaded > 0 ? fmtBytes(d.uploaded) : ""],
      ["Info hash", d.infoHash],
      ["VPN", VPN_TEXT[d.vpn] || "Off"],
      ["Magnet link", d.url],
    ] : [
      ["Save folder", movable || d.folder && d.folder.on ? "" : d.dir], // a folder's place is under Folder; a movable one has Change…
    ];
    // A link stays on one line, cut short; all of it shows on hover. Links and the folder have a copy button, and an open one where they can be opened.
    fresh(fileFacts, [facts, !!can.showInFolder], () => facts.filter(([, v]) => v).flatMap(([k, v]) =>
      [el("dt", {}, k), k === "Save folder" ? linkCell(v, "folder", can.showInFolder && (() => act("showInFolder", { id })), false)
        : k.endsWith("link") ? linkCell(v, "link", null)
        : el("dd", { class: k === "Info hash" ? "mono" : "" }, v)]));
    const nameOfIfc = (ifc) => ifaceName(ifc, latest.state, latest.settings);
    summaryLine.textContent = done ? [finishedSummary(d, nameOfIfc), finishedVerdict(d, latest.state && latest.state.interfaces, nameOfIfc)].filter(Boolean).join(" · ") : "";
    summaryLine.hidden = !summaryLine.textContent;
    fileSection.hidden = !fileFacts.children.length && summaryLine.hidden && dirMove.hidden;

    const mirrors = mirrorRows(d, (ifc) => ifaceName(ifc, latest.state, latest.settings));
    mirrorSection.hidden = !mirrors.length;
    fresh(mirrorList, mirrors, () => mirrors.flatMap((m) => [
      el("span", { class: "mono one-line", title: m.url }, m.host),
      m.dropped ? el("span", { class: "badge bad" }, "Dropped") : el("span", { class: "muted" }, m.users.join(", ") || "unused"),
      m.dropped ? el("div", { class: "why" }, m.dropped) : null,
    ].filter(Boolean)));

    if (torrent) {
      // Torrents keep no log: say so instead of showing nothing.
      activity.say("A torrent keeps no activity log. Its peers and what it has sent are under File.");
      if (details.open && !choosing) tfiles.load();
      else if (choosing) tfiles.section.hidden = true;
    } else activity.load();

    srcSum.update(d, torrent, done);
  }

  return { root, update, number(n) { if (n !== queuePlace) { queuePlace = n; if (d) paintTile(num, tileOf(d), queuePlace, d.label); } } };
}

// A Metalink's files, or a split archive's parts, as one card: their progress added up, pause, resume
// and remove for all of them, and the files' own cards inside.
function makeGroupCard(gid) {
  let g = null; // the group as last shown
  let open = false;
  const ids = () => (g ? g.items.map((i) => i.d.id) : []);
  const each = async (method, params, okText, pick) => {
    // All at once: a pause answers only once its connections have closed.
    await Promise.all(g.items.map((i) => i.d).filter((d) => !pick || pick(d)).map((d) => call(method, { id: d.id, ...params }).catch((e) => toast(e.message, true))));
    if (okText) toast(okText);
  };

  const num = el("span", { class: "n st-tile", role: "img" });
  let queuePlace = 0;
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
  const ask = confirmRow("Remove all these files' downloads?",
    el("button", { class: "small primary", "data-keep": "", onclick: () => each("remove", { deleteFile: false }, "Removed from list (files kept)") }, "Remove, keep files"),
    el("button", { class: "small danger", onclick: () => each("remove", { deleteFile: true }, "Removed, unfinished files deleted") }, "Remove and delete unfinished files"));
  const menuRemove = menuItem("Remove all…", () => {
    if (groupView(g.summary).partialFile) { ask.hidden = false; ask.querySelector("[data-keep]").focus(); }
    else each("remove", { deleteFile: false }, "Removed from list (files kept)");
  });
  const menuAsFile = menuItem("Download as a file instead", () => fold.asFile());
  // A split archive's extraction that seems stuck starts over, as after a restart.
  const menuRetry = menuItem("Retry extraction", () => act("retryExtraction", { id: g.items[0].d.id }, "Extraction started over"));
  const menu = el("div", { class: "menu", hidden: true }, menuRetry, menuAsFile, menuRemove);
  const more = menuButton(() => menu);
  // A split archive is one Download as folder: its switch is here, not on its parts' cards.
  let foldId = "", passId = "";
  const fold = folderSwitch(() => foldId, () => passId);
  const keep = keepSelect(() => foldId);
  const extract = extractBar();
  const toggle = iconButton("chevron", "Show its files", () => { open = !open; update(g); });
  const files = el("div", { class: "group-files" });
  const root = el("div", { class: "card group", tabindex: "-1", "data-group": gid },
    num, name, el("span", { class: "end" }, toggle, pause, resume, el("span", { class: "more" }, more, menu)),
    el("div", { class: "prog" }, bar), el("div", { class: "nums" }, numsMain), el("div", { class: "what" }, status, left),
    extract.bar, extract.line, fold.row, fold.pass, fold.ask, keep.root, ask, files);

  function update(next) {
    g = next;
    const v = groupView(g.summary);
    const ds = g.items.map((i) => i.d);
    title.textContent = name.title = g.summary.name;
    toggle.setAttribute("aria-expanded", String(open));
    root.classList.toggle("sel", ds.every((d) => selected.has(d.id)));
    const parts = g.summary.parts;
    count.textContent = `${g.items.length} ${parts ? "parts" : "files"}`;
    root.classList.toggle("parts", parts);
    const fv = parts ? setFolderView(ds, latest.settings) : null;
    foldId = fv ? fv.id : "";
    passId = fv && fv.password ? fv.password.id : "";
    const xv = parts ? extractView(ds, g.summary.extractEta) : null;
    extract.update(xv);
    fold.update(fv);
    keep.update(fv && fv.keep);
    menuAsFile.hidden = !fold.declinable(fv);
    menuRemove.textContent = v.partialFile ? "Remove all…" : "Remove all";
    menuRetry.hidden = !parts || g.summary.finished || !ds.some((d) => d.folder && d.folder.on);
    status.textContent = v.label;
    status.className = "badge st " + v.tone;
    paintTile(num, v.speed > 0 ? { kind: "run", icon: "down" } : queuePlace ? { kind: "", icon: "" } : { kind: "", icon: "pause" }, queuePlace, v.label);
    fill.style.width = v.percent + "%";
    // Beside the extraction's bar, this one says it is the download's.
    left.textContent = (xv ? "Download · " : "") + v.left;
    numsMain.textContent = v.speed > 0 ? fmtSpeed(v.speed) : "";
    pause.hidden = !v.pause;
    resume.hidden = !v.resume;
    files.hidden = !open;
    let i = 0;
    for (const { d, form } of g.items) {
      const c = cardFor(d.id);
      c.update(d, form);
      place(files, c.root, i++);
    }
    while (files.children.length > i) files.lastChild.remove();
  }

  return { root, update, ids, number(n) { if (n !== queuePlace) { queuePlace = n; if (g) update(g); } } };
}

// makeLight is a queued download nobody has touched (lib/rows.js isLight), drawn
// as its slim card looks but with about a fifth of the elements: its buttons are
// copies that do nothing. promote builds the real card when the pointer or the
// focus comes to the row, before a click can land on a dead button.
let lightEnd = null;
function makeLight(id, promote) {
  if (!lightEnd) {
    lightEnd = el("span", { class: "end" }, ...["chevron:Details", "up:Move up", "down:Move down", "pause:Pause", "play:Resume", "more:More"]
      .map((s) => iconButton(...s.split(":"))));
    for (const b of lightEnd.children) b.tabIndex = -1;
  }
  const end = lightEnd.cloneNode(true);
  const [, up, down, pause, resume] = end.children;
  const num = el("span", { class: "n st-tile", role: "img" });
  const name = el("span", { class: "name", title: "" });
  const size = el("b", { class: "num" });
  const root = el("div", { class: "card one idle", tabindex: "-1", "data-id": id }, num,
    el("span", { class: "title" }, el("span", { class: "grip", title: "Drag onto another queued download to move it" }, icon("grip")), name),
    end, el("div", { class: "nums" }, size));
  for (const ev of ["pointerenter", "focusin"]) root.addEventListener(ev, promote);
  let d = null, queuePlace = 0, sig = "";
  function paint() {
    const can = d.actions || {};
    const at = queue.at.get(id);
    const s = [d.filename, d.url, d.size, d.label, d.tone, queuePlace, can.pause, can.resume, at, queue.len].join("\n");
    if (s === sig) return;
    sig = s;
    paintTile(num, tileOf(d), queuePlace, d.label);
    name.textContent = name.title = d.filename || d.url;
    size.textContent = rowNums(d).main;
    pause.hidden = !can.pause;
    resume.hidden = !can.resume;
    up.style.visibility = at === 0 ? "hidden" : "";
    down.style.visibility = at === queue.len - 1 ? "hidden" : "";
  }
  return {
    root,
    update(next) { d = next; paint(); },
    number(n) { if (n !== queuePlace) { queuePlace = n; if (d) paint(); } },
  };
}

function cardFor(id) {
  let c = cards.get(id);
  if (!c) {
    let line = null, full = null, light = null, place = 0, last = null;
    // The card takes the light row's (or the line's) place, and the focus with it.
    const build = () => {
      full = makeCard(id);
      full.number(place);
      const old = light || line;
      if (old) {
        const focused = old.root.contains(document.activeElement);
        old.root.replaceWith(full.root);
        if (focused) full.root.focus();
      }
      light = line = null;
    };
    c = {
      get root() { return (full || light || line).root; },
      number(n) { place = n; if (full) full.number(n); else if (light) light.number(n); },
      // form is the row's (lib/rows.js): "card", "light" (a card not yet
      // built), or a line ("line", or "part" for a group's fetched part). The
      // DOM follows it.
      update(d, form) {
        last = d;
        if (form === "light" && !full && !line) {
          if (!light) {
            light = makeLight(id, () => { if (!full) { build(); full.update(last); } });
            light.number(place);
          }
          light.update(d);
          return;
        }
        if (form === "card" && !full) build();
        else if (!line && (form === "part" || (form === "line" && !full))) {
          // A download that finishes while its card is shown keeps the card
          // until the page opens again; a group's fetched part turns into a line.
          line = makeLine(id);
          const old = full || light;
          if (old) old.root.replaceWith(line.root);
          full = light = null;
        }
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
  if (!d.partialFile) return removeWithUndo([id]);
  ask.hidden = false;
  ask.querySelector("[data-keep]").focus(); // keeping is the default; deleting takes a deliberate click
}

async function copyLink(url, what = "link") {
  if (!url) return;
  try {
    await navigator.clipboard.writeText(url);
    toast(what === "link" ? "Link copied" : `Copied the ${what}`);
  } catch {
    toast(`Couldn't copy the ${what}`, true);
  }
}

// linkCell is a value in Details' facts that can be copied: its text (one line,
// all of it on hover), a copy button and, given open, one that opens it.
function linkCell(v, what, open, mono = true) {
  return el("dd", { class: "linkrow" },
    el("span", { class: (mono ? "mono " : "") + "one-line", title: v }, v),
    iconButton("copy", `Copy the ${what}`, () => copyLink(v, what)),
    open ? iconButton("open", `Open the ${what}`, open) : null);
}
