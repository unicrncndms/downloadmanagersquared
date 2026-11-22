"use strict";

// The downloads view: the toolbar popup, or a small window when opened from the tray.
// A card shows at a glance what a download is doing; the rest is one click
// away, in its "⋯" menu or its Details. Finished downloads shrink to a line.

let latest = {};
const cards = new Map(); // download id -> card controller
let queue = { at: new Map(), len: 0 }; // each queued download's place in the whole queue, and its length (lib/rows.js)

// Details shows one download beside the list (a wide window) or in a sheet
// over its lower half (the popup): a row never grows, so the list keeps its
// shape. The opened download's row puts its Details into body (card.js).
const detail = { id: "", body: $("detailBody"), title: $("detailTitle") };
// quiet: from inside a draw, which goes on to show it itself.
function showDetail(id, quiet) {
  detail.id = id;
  saveSpot();
  $("detail").hidden = false;
  document.body.classList.add("detailing");
  if (quiet) return;
  if (latest.state) renderDownloads(latest.state);
  // Under the sheet the row would be covered: it goes to the top of what is left of the list.
  const row = $("list").querySelector(".card.detailed");
  if (row && matchMedia("(max-width: 759px)").matches) row.scrollIntoView({ block: "start" });
}
function closeDetail() {
  const row = $("list").querySelector(".card.detailed");
  detail.id = "";
  saveSpot();
  $("detail").hidden = true;
  document.body.classList.remove("detailing");
  if (latest.state) renderDownloads(latest.state);
  if (row) row.focus();
}
$("detailClose").append(icon("close"));

// The list's scroll and the open Details come back when the page opens again.
// Restored once, at the first list drawn; a download gone since opens nothing.
let spotRestored = false;
function saveSpot() {
  if (spotRestored) remember("list", { top: $("list").scrollTop, detail: detail.id });
}
let spotFrame = 0;
$("list").addEventListener("scroll", () => {
  if (!spotFrame) spotFrame = requestAnimationFrame(() => { spotFrame = 0; saveSpot(); });
}, { passive: true });
addEventListener("pagehide", saveSpot);
function restoreSpot(state) {
  if (spotRestored) return;
  const r = recall("list") || {};
  if (r.detail && (state.downloads || []).some((d) => d.id === r.detail)) showDetail(r.detail);
  $("list").scrollTop = r.top || 0;
  spotRestored = true;
}
$("detailClose").onclick = closeDetail;

// The downloads selected, by id: a click selects a row, Ctrl and Shift add to
// it, and with several the bar over the tools acts on all of them.
const selected = new Set();
let anchor = ""; // where a Shift click's range starts
let shown = []; // the ids of each row in the list, in order
let removing = false; // the bar asks whether unfinished files go too

function rowIds(row) {
  if (row.dataset.id) return [row.dataset.id];
  const g = groupCards.get(row.dataset.group);
  return g ? g.ids() : [];
}
const picked = () => ((latest.state && latest.state.downloads) || []).filter((d) => selected.has(d.id));

function select(ids, e) {
  anchor = clickRows(selected, anchor, ids, e, shown);
  removing = false;
  // Details follows the row selected, once it is open.
  if (detail.id && selected.size === 1 && !selected.has(detail.id)) return showDetail([...selected][0]);
  if (latest.state) renderDownloads(latest.state);
}

$("list").addEventListener("mousedown", (e) => { if (e.shiftKey && e.target.closest(".card")) e.preventDefault(); }); // no text selected by a range
$("list").addEventListener("click", (e) => {
  if (e.target.closest("button, a, input, select, textarea, label, .menu, .extract, .pick, .ask")) return;
  const row = e.target.closest(".card");
  if (row) select(rowIds(row), e);
});
$("list").addEventListener("dblclick", (e) => {
  const row = e.target.closest(".card");
  if (row && row.dataset.id && !e.target.closest("button, a, input, label")) openOrDetails(row.dataset.id);
});
// A finished download's row opens the file; any other row, or a file that can't be opened, its Details.
function openOrDetails(id) {
  const d = ((latest.state && latest.state.downloads) || []).find((x) => x.id === id);
  if (d && finished(d) && d.actions && d.actions.open) act("open", { id });
  else showDetail(id);
}
// On a row: the arrows go from row to row, Space selects, Enter opens a finished download and shows another's Details, Delete removes.
$("list").addEventListener("keydown", (e) => {
  const row = e.target;
  if (!row.classList || !row.classList.contains("card")) return;
  const step = { ArrowDown: "nextElementSibling", ArrowUp: "previousElementSibling" }[e.key];
  if (step) {
    let to = row[step];
    while (to && !to.classList.contains("card")) to = to[step];
    if (to) to.focus();
  } else if (e.key === " ") select(rowIds(row), { ctrlKey: true });
  else if (e.key === "Enter" && row.dataset.id) openOrDetails(row.dataset.id);
  else if (e.key === "Delete") {
    if (!rowIds(row).every((id) => selected.has(id))) select(rowIds(row), {});
    removeSelected();
  } else return;
  e.preventDefault();
});
// Ctrl+A selects every row, Ctrl+F or "/" goes to the search, Ctrl+N opens the Add box.
// In a field, Ctrl+A and "/" stay the field's own.
document.addEventListener("keydown", (e) => {
  if (e.defaultPrevented || openMenu || e.altKey) return;
  const inField = e.target.closest && e.target.closest("input, textarea, select, [contenteditable]");
  const ctrl = (e.ctrlKey || e.metaKey) && !e.shiftKey;
  const k = e.key.toLowerCase();
  if (ctrl && k === "n") openAdd(true);
  else if (ctrl && k === "f" || !ctrl && !e.ctrlKey && e.key === "/" && !inField) { $("search").focus(); $("search").select(); }
  else if (ctrl && k === "a" && !inField) { for (const id of shown.flat()) selected.add(id); removing = false; if (latest.state) renderDownloads(latest.state); }
  else return;
  e.preventDefault();
});
document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape" || e.defaultPrevented || openMenu) return;
  if (detail.id) closeDetail();
  else if (selected.size) { selected.clear(); removing = false; renderDownloads(latest.state); }
});

// each sends one request for every selected download that pick takes, at once.
async function each(method, params, pick, okText) {
  const ds = picked().filter(pick || (() => true));
  if (method === "remove") await removeMany(ds.map((d) => d.id), params.deleteFile);
  else await Promise.all(ds.map((d) => call(method, { id: d.id, ...params }).catch((err) => toast(err.message, true))));
  if (okText && ds.length) toast(okText);
}
function removeSelected() {
  const ds = picked();
  if (!ds.length) return;
  if (ds.some((d) => d.partialFile) && !removing) { removing = true; return renderSelBar(); }
  removing = false;
  removeWithUndo(ds.map((d) => d.id));
}

// removeWithUndo takes the downloads off the list at once and says so with an
// Undo; the engine is asked to remove them (files kept) when the note ends.
// Closing the popup first counts as keeping the removal. Undo only brings the
// rows back: until then the engine still has them.
const removal = undoRemoval((ids) => removeMany(ids, false));
const hiding = removal.hiding;
function removeWithUndo(ids) {
  const text = ids.length === 1 ? "Removed from the list (file kept)" : `${plural(ids.length, "download")} removed from the list (files kept)`;
  const node = el("div", { class: "toast show undo", role: "status" }, text,
    el("button", { class: "small", onclick: () => {
      removal.undo();
      if (latest.state) renderDownloads(latest.state);
    } }, "Undo"));
  removal.start(ids, 8000, () => node.remove());
  document.body.append(node);
  if (latest.state) renderDownloads(latest.state);
}
addEventListener("pagehide", () => removal.flush());
// The selected queued downloads start next, in the order they are listed.
function startNext() {
  for (const d of picked().filter((x) => x.status === "queued").reverse()) moveDownload(d.id, "first");
}

function renderSelBar() {
  const bar = $("selBar");
  const ds = picked();
  // One selected row has its own buttons; the bar is for several (or a remove to confirm).
  bar.hidden = ds.length < 2 && !removing;
  if (bar.hidden) return;
  const can = (k) => ds.some((d) => d.actions && d.actions[k]);
  const button = (label, go, cls) => el("button", { class: "small " + (cls || ""), onclick: go }, label);
  const clear = iconButton("close", "Clear the selection", () => { selected.clear(); removing = false; renderDownloads(latest.state); });
  fresh(bar, [ds.length, removing, can("pause"), can("resume"), ds.some((d) => d.status === "queued"), ds.some((d) => d.partialFile)], () => (removing ? [
    el("span", { class: "grow" }, `Remove ${plural(ds.length, "download")}?`),
    button("Keep files", () => { removing = false; each("remove", { deleteFile: false }, null, "Removed from the list (files kept)"); }, "primary"),
    button("Delete unfinished files", () => { removing = false; each("remove", { deleteFile: true }, null, "Removed, unfinished files deleted"); }, "danger"),
    button("Cancel", () => { removing = false; renderSelBar(); }),
  ] : [
    el("span", { class: "grow" }, `${ds.length} selected`),
    can("pause") ? button("Pause", () => each("pause", {}, (d) => d.actions && d.actions.pause)) : null,
    can("resume") ? button("Resume", () => each("resume", {}, (d) => d.actions && d.actions.resume)) : null,
    ds.some((d) => d.status === "queued") ? button("Move to top", startNext) : null,
    button(ds.some((d) => d.partialFile) ? "Remove…" : "Remove", removeSelected),
    clear,
  ].filter(Boolean)));
}

// dropOn: queued download from is dropped on target. Dragged from a
// selection, the selected queued downloads go together, in their order.
function dropOn(from, target) {
  const all = latest.state.downloads || [];
  const group = selected.has(from) ? all.filter((d) => selected.has(d.id) && d.status === "queued").map((d) => d.id) : [];
  const running = all.some((d) => d.id === target && d.status === "running");
  if (group.length < 2 || running || group.includes(target)) return moveDownload(from, target);
  const before = moveBefore(all, group[0], target);
  if (before === null) return;
  for (const id of group) if (id !== before) act("move", { id, before });
}

// The connections in the header: each one's colour, name and speed now. One
// that carries nothing while downloads run shows a hollow square and "idle":
// a dead line is seen without its colour.
function renderStrip(s) {
  const ifs = (s.interfaces || []).filter((i) => i.present !== false && i.enabled);
  const idle = (i) => !!s.active && !(i.speed > 0);
  // Held back on some download right now: the server busy, or backing off.
  const busy = new Set((s.downloads || []).flatMap((d) => Object.keys(d.links || {}).filter((id) => d.links[id].tone === "warn")));
  const many = ifs.length > 2, wasMany = $("strip").classList.contains("many");
  $("strip").classList.toggle("many", many);
  const changed = fresh($("strip"), ifs.map((i) => [i.id, i.name, idle(i), busy.has(i.id), speedUnitLabel()]), () => ifs.map((i, n) => {
    const sq = el("i", { class: "dot" });
    sq.style.color = ifaceHue(i.id, s);
    // Busy is a ring on the square, as a row's busy connection tile; the word stays in the label.
    const isBusy = busy.has(i.id), isIdle = idle(i) && !isBusy;
    const said = isIdle ? "idle" : fmtSpeed(i.speed || 0) + (isBusy ? ", server busy" : "");
    return el("button", { class: "chip" + (isBusy ? " busy" : isIdle ? " idle" : ""), title: `${i.name}: ${said}`, "aria-label": `${i.name}, ${said}. Open Connections`,
      onclick: () => { Host.openSettings("#connections"); if (!document.body.classList.contains("windowed")) window.close(); } },
      sq, el("span", { class: "nm" }, i.name), el("span", { class: "nm short" }, i.windowsName && i.windowsName !== i.name ? i.name : shortName(i.name)),
      el("span", { class: "muted num" }, isIdle ? "idle" : stripSpeed(i.speed || 0) + (n === 0 ? " " + speedUnitLabel() : "")),
      isBusy ? el("span", { class: "busy" }, "busy") : null);
  }));
  // The speeds change every second: written into the chips as they are, so hover and focus stay.
  let wider = false; // a digit more may no longer fit the header's line
  if (!changed) ifs.forEach((i, n) => {
    const chip = $("strip").children[n], isBusy = busy.has(i.id);
    if (idle(i) && !isBusy) return;
    const said = fmtSpeed(i.speed || 0) + (isBusy ? ", server busy" : "");
    chip.title = `${i.name}: ${said}`;
    chip.setAttribute("aria-label", `${i.name}, ${said}. Open Connections`);
    const speed = chip.querySelector(".muted"), text = stripSpeed(i.speed || 0) + (n === 0 ? " " + speedUnitLabel() : "");
    wider ||= text.length !== speed.textContent.length;
    speed.textContent = text;
  });
  if (changed || wider || many !== wasMany) fitStrip(); // measuring forces a layout: only when the strip changed (and on resize)
}

// fitStrip keeps the strip on one line. The window's gets the short names
// when the full ones don't fit, and only the colours and speeds when the
// short ones don't either; the popup's, which has the short names already,
// first drops "idle" and "busy", then the names (each still says its name on hover).
function fitStrip() {
  const strip = $("strip");
  // The chips reach 4px past the strip by design (their hover box); that is not running out of room.
  const over = () => strip.scrollWidth > strip.clientWidth + 5;
  strip.classList.remove("tight", "tighter");
  if (over()) strip.classList.add("tight");
  if (over()) strip.classList.add("tighter");
}
addEventListener("resize", fitStrip);
document.fonts.ready.then(fitStrip); // the first measure may come before Rubik: its letters are wider

if (new URLSearchParams(location.search).has("window")) document.body.classList.add("windowed");

// In the Downloads window the header matches the settings page's.
if (windowHeader()) $("openSettings").hidden = true; // its tabs lead there
$("openSettings").append(icon("gear"));

$("openSettings").onclick = () => {
  Host.openSettings("");
  if (!document.body.classList.contains("windowed")) window.close();
};
$("pauseAll").onclick = () => act("pauseAll");
$("resumeAll").onclick = () => act("resumeAll");
// The Add box: closed again by + Add or Escape.
// What the Add box holds, kept while Windows' folder picker closes the popup. No passwords.
const addDraft = () => ({
  url: $("addUrl").value, page: $("addPage").value, vpn: $("addVpn").value,
  name: $("addName").value, sum: $("addSum").value, folder: $("addFolder").checked, dir: addDir.get(),
});
const addDir = folderControl(undefined, addDraft);
$("addFolderRow").after(addDir.root);
// Back from that picker: the Add box as it was, in the folder picked. Once,
// when the engine's settings are in (the box reads them).
let draftTaken = !Host.takeDraft;
function restoreAddDraft() {
  if (draftTaken) return;
  draftTaken = true;
  Host.takeDraft().then((d) => {
    if (!d) return;
    $("addUrl").value = d.url || "";
    $("addPage").value = d.page || "";
    $("addVpn").value = d.vpn || "";
    $("addName").value = d.name || "";
    $("addSum").value = d.sum || "";
    openAdd(true);
    $("addUrl").dispatchEvent(new Event("input"));
    $("addFolder").checked = !!d.folder; // after input, which sets the default
    if (d.dir) addDir.set(d.dir);
  }, () => {});
}
function openAdd(on) {
  if (on) addDir.paint(); // the download folder may have changed
  $("addBox").hidden = !on;
  $("toggleAdd").setAttribute("aria-expanded", String(on));
  if (on) $("addUrl").focus();
  else $("toggleAdd").focus();
}
$("toggleAdd").onclick = () => openAdd($("addBox").hidden);
$("addBox").addEventListener("keydown", (e) => {
  if (e.target === $("addUrl") && addSubmits(e, $("addUrl").value)) {
    e.preventDefault();
    $("addBox").requestSubmit();
    return;
  }
  if (e.key !== "Escape") return;
  e.preventDefault();
  openAdd(false);
});
// Links pasted anywhere but a field open the box with them in it.
document.addEventListener("paste", (e) => {
  if (e.target.closest && e.target.closest("input, textarea, select, [contenteditable]")) return;
  const text = e.clipboardData.getData("text");
  if (!addView(text).count) return;
  e.preventDefault();
  const box = $("addUrl");
  box.value = [box.value.trim(), text.trim()].filter(Boolean).join("\n");
  openAdd(true);
  box.dispatchEvent(new Event("input"));
});
// A .zip, .7z or .rar link offers Download as folder, ticked as the settings default.
const zipLine = (url) => addFolderChoice(url, latest.settings).show;
$("addUrl").addEventListener("input", () => {
  // The landing page and the VPN once a link they apply to is in the box.
  const v = addView($("addUrl").value);
  const single = v.count + v.bad <= 1; // a name and a checksum belong to one link
  $("addPage").parentElement.hidden = !v.page;
  $("addVpn").parentElement.hidden = !v.vpn;
  $("addNameRow").hidden = $("addSumRow").hidden = !single;
  const link = $("addUrl").value.trim();
  $("addUserRow").hidden = $("addPassRow").hidden = !(single && /^(s?ftp|ftps):\/\//i.test(link));
  $("addKeyRow").hidden = $("addKeyPassRow").hidden = !(single && /^sftp:\/\//i.test(link));
  $("addVpn").options[0].textContent = `As in settings (${VPN_TEXT[latest.settings.vpnHttp] || "Off"})`;
  $("addGo").textContent = v.label;
  $("addOptions").hidden = !(single || v.page || v.vpn);
  $("addUrl").rows = Math.min(5, Math.max(2, $("addUrl").value.split(/\n/).length)); // grows with the links, to five lines
  $("addHint").textContent = plainFtpNote(single ? withLogin(link, "", $("addPass").value) : $("addUrl").value) || (v.bad ? `${plural(v.bad, "line")} that isn't a link will stay in the box.` : v.count ? "" : "Paste links here, or anywhere on the list with Ctrl+V.");
  const c = addFolderChoice("", latest.settings);
  const show = addLines($("addUrl").value).urls.some((u) => zipLine(u.url));
  if (show === !$("addFolderRow").hidden) return; // leave a box the user ticked alone
  $("addFolderRow").hidden = !show;
  $("addFolder").checked = c.checked;
});
$("addPass").addEventListener("input", () => $("addUrl").dispatchEvent(new Event("input"))); // the ftp:// warning
// Several links, one per line: each is added as if alone. Lines that fail
// stay in the box, so they can be fixed and added again.
// A link already in the list, or whose file is already in the folder, is held
// by the engine and asked about above the list (asks.js).
$("addBox").addEventListener("submit", async (e) => {
  e.preventDefault();
  const { urls, bad } = addLines($("addUrl").value);
  const many = urls.length + bad.length > 1;
  const failed = bad.map((b) => ({ ...b, error: "not a link" }));
  let asked = 0;
  for (const { line, url } of urls) {
    const add = { url: many ? url : withLogin(url, $("addUser").value.trim(), $("addPass").value), pageUrl: $("addPage").value, vpn: $("addVpn").value, checkDup: true, ask: true };
    if (!many && $("addName").value.trim()) add.filename = $("addName").value.trim();
    if (!many && $("addSum").value.trim()) add.checksum = $("addSum").value.trim();
    if (!many && $("addKey").value.trim() && /^sftp:/i.test(url)) { add.keyFile = $("addKey").value.trim(); add.keyPass = $("addKeyPass").value; }
    if (Host.userAgent !== null) add.userAgent = Host.userAgent; // else the engine uses the browser's
    if (addDir.get()) add.dir = addDir.get(); // else the download folder
    const folder = addFolderParam(!$("addFolderRow").hidden && zipLine(url), $("addFolder").checked, latest.settings);
    if (folder !== undefined) add.folder = folder;
    try {
      const r = await call("add", add);
      if (r && r.skipped) failed.push({ line, url, error: r.skipped }); // stays in the box, with why
      else if (r && r.ask) asked++; // held until answered above the list
    } catch (err) {
      failed.push({ line, url, error: err.message });
    }
  }
  const added = urls.length + bad.length - failed.length;
  if (failed.length) {
    $("addUrl").value = failed.sort((a, b) => a.line - b.line).map((f) => f.url).join("\n");
    const why = failed.map((f) => (many ? `Line ${f.line}: ${f.error}` : f.error)).join(" · ");
    toast(added ? `${plural(added, "download")} added. ${why}` : why, true);
    return;
  }
  toast(asked ? `${plural(asked, "download")} waiting for your answer` : added === 1 ? "Added" : `${added} downloads added`);
  $("addUrl").value = "";
  $("addPage").value = $("addName").value = $("addSum").value = $("addKey").value = $("addKeyPass").value = $("addUser").value = $("addPass").value = "";
  addDir.reset();
  $("addUrl").dispatchEvent(new Event("input"));
  openAdd(false);
});

// Local .torrent and Metalink files dropped on the list.
// Their bytes go to the engine as base64.
async function addFiles(files) {
  let added = 0, failed = false;
  for (const f of files) {
    try {
      const why = addFileCheck(f.name, f.size);
      if (why) throw new Error(why);
      const file = await new Promise((ok, no) => {
        const r = new FileReader();
        r.onload = () => ok(String(r.result).split(",")[1]);
        r.onerror = () => no(new Error(`${f.name} could not be read`));
        r.readAsDataURL(f);
      });
      const add = { file, fileName: f.name, vpn: $("addVpn").value };
      if (addDir.get()) add.dir = addDir.get();
      await call("add", add);
      added++;
    } catch (err) {
      failed = true;
      toast(err.message, true);
    }
  }
  if (added && !failed) toast(added === 1 ? "Added" : `${added} downloads added`);
}
// Windows' file picker, which the engine shows and adds from: Firefox closes
// the toolbar popup when a dialog opens, so a page's own file input can't.
$("addFileBtn").onclick = () => act("addFromFiles", { vpn: $("addVpn").value, dir: addDir.get() });
// Dropped on the page: files are added, links go into the Add box. A row being dragged is the queue's own.
const dropsHere = (e) => latest.state && !e.dataTransfer.types.includes(DRAG_TYPE)
  && ["Files", "text/uri-list", "text/plain"].some((t) => e.dataTransfer.types.includes(t));
document.addEventListener("dragover", (e) => {
  if (!dropsHere(e)) return;
  e.preventDefault();
  document.body.classList.add("dropping");
});
document.addEventListener("dragleave", (e) => { if (!e.relatedTarget) document.body.classList.remove("dropping"); });
document.addEventListener("drop", (e) => {
  document.body.classList.remove("dropping");
  if (!dropsHere(e)) return;
  e.preventDefault();
  if (e.dataTransfer.files.length) return addFiles([...e.dataTransfer.files]);
  const text = (e.dataTransfer.getData("text/uri-list") || e.dataTransfer.getData("text/plain")).replace(/^#.*$/gm, "").trim();
  if (!addView(text).count) return;
  const box = $("addUrl");
  box.value = [box.value.trim(), text].filter(Boolean).join("\n");
  openAdd(true);
  box.dispatchEvent(new Event("input"));
});

// The list's sections (lib/rows.js): a heading with how many it
// holds, which folds its rows away. Folded ones are kept in the engine's settings (foldedSections), shared by the popup and the window.
const folded = new Set();
let foldWrites = 0; // while one is on its way the engine's older list isn't taken back
function syncFolded(settings) {
  if (foldWrites || !settings) return;
  folded.clear();
  for (const n of settings.foldedSections || []) folded.add(n);
}
const heads = new Map(); // section name -> { root, count, button }
function sectionHead(name) {
  let h = heads.get(name);
  if (!h) {
    const button = el("button", { onclick: () => {
      folded.has(name) ? folded.delete(name) : folded.add(name);
      foldWrites++;
      call("setSettings", { foldedSections: [...folded] }).catch(() => {}).finally(() => foldWrites--);
      renderDownloads(latest.state);
    } });
    button.className = name === "Needs you" ? "mk bad" : name === "Finished" ? "mk ok" : "mk";
    h = { button, count: el("span", { class: "count num" }),
      // How many run at once, a link to the setting.
      limit: name === "Queue" ? el("a", { href: "#queue", title: "Downloads at once, in the settings", onclick: (e) => {
        e.preventDefault();
        Host.openSettings("#queue");
        if (!document.body.classList.contains("windowed")) window.close();
      } }) : null };
    button.append(name, h.count, icon("chevron"));
    h.root = el("h2", { class: "sec" }, button,
      name === "Finished" ? el("a", { href: "#", onclick: (e) => { e.preventDefault(); clearFinished(); } }, "Clear finished") : null,
      h.limit);
    heads.set(name, h);
  }
  h.button.setAttribute("aria-expanded", String(!folded.has(name)));
  return h;
}

// arrange puts the nodes wanted into the list in order and takes out the
// rest. A row that changes place slides there, so the eye can follow it.
const calm = matchMedia("(prefers-reduced-motion: reduce)");
let arranged = [];
function arrange(list, want) {
  if (want.length === arranged.length && want.every((n, i) => n === arranged[i] && n.parentNode === list)) return;
  const was = new Map();
  if (!calm.matches && arranged.length && want.length < 400) for (const n of list.children) was.set(n, n.offsetTop);
  let at = list.firstElementChild;
  for (const node of want) {
    if (at === node) at = at.nextElementSibling;
    else list.insertBefore(node, at);
  }
  while (at) {
    const next = at.nextElementSibling;
    at.remove();
    at = next;
  }
  arranged = want;
  for (const [n, top] of was) {
    const dy = n.parentNode === list ? top - n.offsetTop : 0;
    if (dy) n.animate([{ transform: `translateY(${dy}px)` }, { transform: "none" }], { duration: 180, easing: "cubic-bezier(.2, .8, .2, 1)" });
  }
}

async function clearFinished() {
  const done = (latest.state.downloads || []).filter((d) => finished(d) && !hiding.has(d.id));
  if (done.length) removeWithUndo(done.map((d) => d.id));
}

const groupCards = new Map(); // group id (a Metalink or a split archive) -> card controller

// The search box narrows the list as it is typed.
$("search").addEventListener("input", () => { if (latest.state) renderDownloads(latest.state); });

let announced = null; // id -> status last seen, for the screen reader's news

function renderDownloads(state) {
  if (hiding.size) state = { ...state, downloads: (state.downloads || []).filter((d) => !hiding.has(d.id)) };
  const list = $("list");
  const all = state.downloads || [];
  const news = announcements(announced, all);
  announced = news.seen;
  if (news.texts.length) $("announce").textContent = news.texts.join(". ");
  const query = $("search").value;
  const res = listRows(state, { query, folded, selected, opened: detail.id });
  for (const id of selected) if (!res.selected.has(id)) selected.delete(id);
  if (res.openedGone) {
    detail.id = "";
    $("detail").hidden = true;
    document.body.classList.remove("detailing");
  }
  const want = []; // the list's nodes, in order
  shown = res.shown;
  queue = res.queue;
  for (const s of res.sections) {
    const head = sectionHead(s.name);
    head.count.textContent = s.count;
    if (head.limit) head.limit.textContent = `${(latest.settings || {}).maxParallel || 1} at a time`;
    want.push(head.root);
    for (const row of s.rows) {
      // A folded section draws nothing, so it updates nothing; the opened download's Details are drawn wherever its row is.
      if (s.folded && !(row.d ? row.d.id === detail.id : row.items.some((i) => i.d.id === detail.id))) continue;
      let c;
      if (row.d) {
        c = cardFor(row.d.id);
        c.update(row.d, row.form);
      } else {
        c = groupCards.get(row.group);
        if (!c) groupCards.set(row.group, c = makeGroupCard(row.group));
        c.update(row);
      }
      c.number(row.place);
      if (!s.folded) want.push(c.root);
    }
  }
  // A controller goes with its download, not with the search: what the search hides keeps its card.
  const have = new Set(all.map((d) => d.id));
  const haveGroups = new Set(all.map((d) => d.group));
  for (const id of cards.keys()) if (!have.has(id)) cards.delete(id);
  for (const id of groupCards.keys()) if (!haveGroups.has(id)) groupCards.delete(id);
  renderSelBar();
  const emptyText = `No downloads match “${query.trim()}”.`;
  if (!all.length) want.push(welcome(state, (latest.settings || {}).thresholdBytes));
  else if (!res.matched) {
    if (emptyNote.textContent !== emptyText) emptyNote.textContent = emptyText;
    want.push(emptyNote);
  }
  arrange(list, want);
  // One row takes the Tab key; the arrows go on from it.
  if (!list.querySelector('.card[tabindex="0"]')) {
    const first = list.querySelector(".card");
    if (first) first.tabIndex = 0;
  }
}
const emptyNote = el("div", { class: "empty", role: "status" });

// field is a row of squares: what an empty list waits to fill, or (off)
// hollow, with the one that failed in red, while the engine is away.
function field(off) {
  return el("div", { class: "field" + (off ? " off" : ""), "aria-hidden": "true" },
    Array.from({ length: 48 }, (_, k) => {
      const i = el("i");
      if (off && k === 22) i.className = "x";
      return i;
    }));
}

// The empty list: how many connections stand ready, how to add a link, and
// what DMS² takes on by itself.
let welcomeNode = null, welcomeSig = "";
function welcome(state, threshold) {
  const ready = (state.interfaces || []).filter((i) => !i.tunnel && i.present !== false && i.enabled);
  const note = linesNote(ready);
  const sig = ready.map((i) => i.id + (i.sameRouter || []).join()).join() + "|" + threshold;
  if (welcomeNode && sig === welcomeSig) return welcomeNode;
  welcomeSig = sig;
  const f = field(false);
  // The connections' colours in a few squares: they are what fills a file.
  [3, 4, 20, 19, 35].forEach((k, n) => { if (ready.length) f.children[k].style.background = ifaceHue(ready[n % ready.length].id, state); });
  const feat = (name, text) => [el("span", { class: "st-tile seed" }, icon(name)), el("span", {}, text)];
  welcomeNode = el("div", { class: "void", role: "status" }, f,
    el("div", { class: "big" }, ready.length > 1 && !note.shared ? `${ready.length} connections ready to fill one file together.` : "Ready for your first download."),
    el("div", { class: "muted lines-note" }, note.text, " ",
      note.add ? el("a", { href: "#", onclick: (e) => { e.preventDefault(); Host.openSettings("#connections"); if (!document.body.classList.contains("windowed")) window.close(); } }, "Add a second line…") : null,
      note.check ? el("button", { class: "linklike", onclick: async (e) => {
        const b = e.target;
        b.disabled = true;
        try {
          const r = lineResults(await call("checkLines", {}));
          b.replaceWith(...[...r.lines, ...r.warnings].map((t) => el("div", {}, t)));
        } catch (err) { b.disabled = false; toast(err.message, true); }
      } }, "Test if they are separate lines") : null),
    el("div", { class: "go" }, el("button", { class: "primary", onclick: () => openAdd(true) }, "+ Add a link"),
      el("span", { class: "muted" }, "or ", el("kbd", {}, "Ctrl"), "+", el("kbd", {}, "V"), " anywhere here")),
    el("div", { class: "feats" },
      feat("down", `Your browser's downloads over ${fmtBytes(threshold || 100e6)} come here by themselves`),
      feat("link", "Magnet links, .torrent and Metalink work too"),
      feat("box", ".zip, .7z and .rar can be extracted while they download")));
  return welcomeNode;
}

// place puts node at index in parent, moving it only if it isn't there:
// moving an element takes the focus out of a field in it and closes a
// dropdown, and the downloads are redrawn every second.
function place(parent, node, index) {
  const at = parent.children[index];
  if (at !== node) parent.insertBefore(node, at || null);
}

// A line under the header when engine and extension may not work together:
// it says which is older and what to do for that one. An extension one minor
// ahead works with its engine (the stores go before GitHub): a soft note only.
function renderVersion(m) {
  const ext = Host.version;
  const clash = m.connected && versionsClash(m.engineVersion, ext);
  const behind = m.connected && engineBehind(m.engineVersion, ext);
  $("versionWarn").hidden = !clash && !behind;
  if (!clash) {
    if (behind) $("versionWarn").textContent = "A newer engine is on its way. This one keeps working.";
    return;
  }
  $("versionWarn").replaceChildren(...(olderSide(m.engineVersion, ext) === "engine"
    ? [`The engine (v${m.engineVersion}) is older than the extension (v${ext}), so they may not work together. Download the new engine: `, link(ENGINE_ZIP_URL, "downloadmanagersquared-windows.zip"), ", extract it and run dms-engine.exe."]
    : [`The extension (v${ext}) is older than the engine (v${m.engineVersion}), so they may not work together. Update it in Firefox: about:addons, then Check for Updates.`]));
}

// A line under the header while downloads under VPN only stopped because the
// VPN dropped (the engine's vpnDown), and a toast once they go on.
let vpnWasDown = false;
function renderVPN(m) {
  const down = !!(m.connected && m.state && m.state.vpnDown);
  $("vpnWarn").hidden = !down;
  if (vpnWasDown && !down && m.connected) toast("VPN connected again. Downloads continue.");
  vpnWasDown = down;
}

// The note under the header: once after an upgrade "Updated to vX: what's new"
// (seenVersion), else a newer release as the engine found it on GitHub (at most
// once a day). Closing either is kept in the engine's settings (seenVersion,
// dismissedUpdate), so it stays closed in the popup and the window.
const closedNotes = new Set(); // closed, though the engine hasn't said so yet
let recordedSeen = "";
function renderUpdate(m) {
  const note = $("updateNote");
  const s = m.settings;
  const u = m.connected && m.state && m.state.update;
  let want = null;
  if (m.connected && s) {
    const act = updateNote(s.seenVersion, m.engineVersion);
    if (act === "record" && recordedSeen !== m.engineVersion) { // a first install has nothing to announce
      recordedSeen = m.engineVersion;
      call("setSettings", { seenVersion: m.engineVersion }).catch(() => {});
    }
    if (act === "show") {
      want = { key: "seen" + m.engineVersion, text: [`Updated to v${m.engineVersion}: `, link(releaseNotesURL(RELEASES_URL, m.engineVersion), "what's new")], save: { seenVersion: m.engineVersion } };
    } else if (u && u.version && u.version !== s.dismissedUpdate) {
      want = { key: "new" + u.version, sig: `${u.installing}|${u.error}`, save: { dismissedUpdate: u.version },
        text: [`DMS² v${u.version} is available: `,
          el("button", { class: "primary small", disabled: !!u.installing, onclick: () => call("installUpdate", {}).catch((e) => toast(e.message, true)) }, u.installing ? "Downloading update…" : "Update now"),
          " ", link(u.url, "see the release"), u.error ? el("div", { role: "alert" }, u.error) : null] };
    }
  }
  if (want && closedNotes.has(want.key)) want = null;
  note.hidden = !want;
  if (!want || (note.dataset.key === want.key && note.dataset.sig === (want.sig || ""))) return;
  note.dataset.key = want.key;
  note.dataset.sig = want.sig || "";
  note.replaceChildren(el("span", {}, ...want.text),
    iconButton("close", "Dismiss this note", () => {
      closedNotes.add(want.key);
      note.hidden = true;
      call("setSettings", want.save).catch(() => {});
    }));
}

// A click shows at once: the requests still on their way are laid over the
// engine's state as what they will do (predict), until a state that came
// after the engine's answer has it. One the engine refuses is taken back.
const pending = []; // { method, params, answered }
let engine = null; // what the engine last said, with nothing laid over it

onCall = (method, params, answer) => {
  if (!engine || !engine.connected || !latest.state || predict(latest.state, method, params) === latest.state) return;
  const e = { method, params, answered: false };
  pending.push(e);
  answer.then(() => { e.answered = true; }, () => { pending.splice(pending.indexOf(e), 1); draw(); });
  setTimeout(() => { e.answered = true; }, 10000); // an answer that never comes isn't believed for ever
  draw();
};

function draw() {
  const m = engine;
  render(pending.length && m.connected && m.state
    ? { ...m, state: pending.reduce((s, e) => predict(s, e.method, e.params), m.state) } : m);
}

connectUI((m) => {
  if (!engine || m.state !== engine.state) {
    for (let i = pending.length - 1; i >= 0; i--) if (pending[i].answered) pending.splice(i, 1);
  }
  engine = m;
  draw();
});

function render(m) {
  latest = m;
  renderConn(m);
  renderVersion(m);
  renderVPN(m);
  syncFolded(m.settings);
  renderUpdate(m);
  renderAsks(m, (id) => { showDetail(id); const c = cards.get(id); if (c) c.root.scrollIntoView({ block: "nearest" }); });
  // Connected, yet with an error: what is shown is out of date (see TOO_LONG in background/engine.js).
  $("staleWarn").hidden = !(m.connected && m.error);
  $("staleWarn").textContent = (m.connected && m.error) || "";
  document.querySelector(".tools").hidden = !m.connected || !m.state;
  if (!m.connected || !m.state) $("addBox").hidden = true;
  if (!m.connected || !m.state) {
    $("list").replaceChildren(m.notInstalled
      ? el("div", { class: "offline" }, field(true),
        el("div", { class: "big" }, "The download engine isn't installed."),
        el("div", {}, "DMS² needs its engine on this PC. ", el("a", { href: "welcome.html", target: "_blank" }, "Get it in two steps"), "."))
      : m.quit && Host.startEngine
      ? el("div", { class: "offline calm" }, field(true),
        el("div", { class: "big" }, "DMS² was quit from its tray icon."),
        el("div", {}, "Your browser handles downloads itself until it runs again."),
        el("div", { class: "actions" }, el("button", { class: "primary", onclick: () => Host.startEngine() }, "Start DMS²")))
      : el("div", { class: "offline", role: "alert" }, field(true),
        el("div", { class: "big" }, "The download engine isn't reachable."),
        el("div", {}, "Make sure DMS² is running in the tray. If it isn't, start it from the Start menu, or download it again from the ", link(RELEASES_URL, "release page"), "."),
        el("div", { class: "actions" },
          Host.startEngine ? el("button", { class: "primary", onclick: () => Host.startEngine() }, "Start DMS²") : null,
          el("button", { onclick: () => location.reload() }, "Check again")),
        m.error ? el("details", {}, el("summary", {}, "Details"), el("div", { class: "muted mono" }, m.error)) : null));
    cards.clear();
    groupCards.clear();
    arranged = [];
    fresh($("strip"), [], () => []);
    return;
  }
  if ($("list").querySelector(".offline")) $("list").replaceChildren();
  if (m.settings) restoreAddDraft();
  const s = m.state;
  fresh($("total"), s.active ? [fmtSpeed(s.speed), fmtETA(s.eta)] : [], () => s.active
    ? [el("b", {}, fmtSpeed(s.speed)), el("small", {}, `${fmtETA(s.eta)} left`)]
    : [el("small", {}, "Idle")]);
  const bulk = bulkActions(s.downloads || []);
  $("pauseAll").disabled = !bulk.pauseAll; // a fixed pair: the toolbar keeps its width
  $("resumeAll").disabled = !bulk.resumeAll;
  renderStrip(s);
  renderDownloads(s);
  restoreSpot(s);
}
