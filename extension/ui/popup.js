"use strict";

// The downloads view: the toolbar popup, or a small window when opened from the tray.
// A card shows at a glance what a download is doing; the rest is one click
// away, in its "⋯" menu or its Details. Finished downloads shrink to a line.

let latest = {};
const cards = new Map(); // download id -> card controller

// Details shows one download beside the list (a wide window) or in a sheet
// over its lower half (the popup): a row never grows, so the list keeps its
// shape. The opened download's row puts its Details into body (card.js).
const detail = { id: "", body: $("detailBody"), title: $("detailTitle") };
// quiet: from inside a draw, which goes on to show it itself.
function showDetail(id, quiet) {
  detail.id = id;
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
  $("detail").hidden = true;
  document.body.classList.remove("detailing");
  if (latest.state) renderDownloads(latest.state);
  if (row) row.focus();
}
$("detailClose").append(icon("close"));
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
  if (e.shiftKey && anchor) {
    const flat = shown.flat();
    const [a, b] = [flat.indexOf(anchor), flat.indexOf(ids[0])].sort((x, y) => x - y);
    if (a >= 0) for (const id of flat.slice(a, b + 1)) selected.add(id);
    for (const id of ids) selected.add(id);
  } else if (e.ctrlKey || e.metaKey) {
    const on = ids.every((id) => selected.has(id));
    for (const id of ids) on ? selected.delete(id) : selected.add(id);
    anchor = ids[0];
  } else {
    const only = selected.size === ids.length && ids.every((id) => selected.has(id));
    selected.clear();
    if (!only) for (const id of ids) selected.add(id);
    anchor = ids[0];
  }
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
  if (row && row.dataset.id && !e.target.closest("button, a, input, label")) showDetail(row.dataset.id);
});
// On a row: the arrows go from row to row, Space selects, Enter opens Details, Delete removes.
$("list").addEventListener("keydown", (e) => {
  const row = e.target;
  if (!row.classList || !row.classList.contains("card")) return;
  const step = { ArrowDown: "nextElementSibling", ArrowUp: "previousElementSibling" }[e.key];
  if (step) {
    let to = row[step];
    while (to && !to.classList.contains("card")) to = to[step];
    if (to) to.focus();
  } else if (e.key === " ") select(rowIds(row), { ctrlKey: true });
  else if (e.key === "Enter" && row.dataset.id) showDetail(row.dataset.id);
  else if (e.key === "Delete") {
    if (!rowIds(row).every((id) => selected.has(id))) select(rowIds(row), {});
    removeSelected();
  } else return;
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
  await Promise.all(ds.map((d) => call(method, { id: d.id, ...params }).catch((err) => toast(err.message))));
  if (okText && ds.length) toast(okText);
}
function removeSelected() {
  const ds = picked();
  if (!ds.length) return;
  if (ds.some((d) => d.partialFile) && !removing) { removing = true; return renderSelBar(); }
  removing = false;
  each("remove", { deleteFile: false }, null, `${plural(ds.length, "download")} removed from the list (files kept)`);
}
// The selected queued downloads start next, in the order they are listed.
function startNext() {
  for (const d of picked().filter((x) => x.status === "queued").reverse()) moveDownload(d.id, "first");
}

function renderSelBar() {
  const bar = $("selBar");
  const ds = picked();
  bar.hidden = ds.length < 2 && !removing;
  if (bar.hidden) return;
  const can = (k) => ds.some((d) => d.actions && d.actions[k]);
  const button = (label, go, cls) => el("button", { class: "small " + (cls || ""), onclick: go }, label);
  const clear = iconButton("close", "Clear the selection", () => { selected.clear(); removing = false; renderDownloads(latest.state); });
  fresh(bar, [ds.length, removing, can("pause"), can("resume"), ds.some((d) => d.status === "queued")], () => (removing ? [
    el("span", { class: "grow" }, `Remove ${plural(ds.length, "download")}?`),
    button("Keep files", () => { removing = false; each("remove", { deleteFile: false }, null, "Removed from the list (files kept)"); }, "primary"),
    button("Delete unfinished files", () => { removing = false; each("remove", { deleteFile: true }, null, "Removed, unfinished files deleted"); }, "danger"),
    button("Cancel", () => { removing = false; renderSelBar(); }),
  ] : [
    el("span", { class: "grow" }, `${ds.length} selected`),
    can("pause") ? button("Pause", () => each("pause", {}, (d) => d.actions && d.actions.pause)) : null,
    can("resume") ? button("Resume", () => each("resume", {}, (d) => d.actions && d.actions.resume)) : null,
    ds.some((d) => d.status === "queued") ? button("Start next", startNext) : null,
    button("Remove…", removeSelected),
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

// shortName fits a connection's name into the popup's strip: its first word,
// cut to three letters when long ("Ethernet" Eth, "Wi-Fi" stays).
function shortName(name) {
  const w = String(name).split(/\s+/)[0];
  return w.length > 6 ? w.slice(0, 3) : w;
}

// stripSpeed is a speed in MB/s with no unit, short enough for the strip:
// 4.4, 87, and from 1 GB/s on 1.2G.
function stripSpeed(bps) {
  const mb = bps / 1e6;
  return mb >= 1000 ? (mb / 1000).toFixed(1) + "G" : mb.toFixed(mb < 100 ? 1 : 0);
}

// The connections in the header: each one's colour, name and speed now. One
// that carries nothing while downloads run shows a hollow square and "idle":
// a dead line is seen without its colour.
function renderStrip(s) {
  const ifs = (s.interfaces || []).filter((i) => i.present !== false && i.enabled);
  const idle = (i) => !!s.active && !(i.speed > 0);
  // Held back on some download right now: the server busy, or backing off.
  const busy = new Set((s.downloads || []).flatMap((d) => Object.keys(d.links || {}).filter((id) => d.links[id].tone === "warn")));
  $("strip").classList.toggle("many", ifs.length > 2);
  fresh($("strip"), ifs.map((i) => [i.id, i.name, Math.round((i.speed || 0) / 1e5), idle(i), busy.has(i.id)]), () => ifs.map((i) => {
    const sq = el("i", { class: "dot" });
    sq.style.color = ifaceHue(i.id, s);
    const said = idle(i) ? "idle" : fmtSpeed(i.speed || 0) + (busy.has(i.id) ? ", server busy" : "");
    return el("button", { class: "chip" + (idle(i) ? " idle" : ""), title: `${i.name}: ${said}`, "aria-label": `${i.name}, ${said}. Open Connections`,
      onclick: () => { Host.openSettings("#connections"); if (!document.body.classList.contains("windowed")) window.close(); } },
      sq, el("span", { class: "nm" }, i.name), el("span", { class: "nm short" }, shortName(i.name)),
      el("span", { class: "muted num" }, idle(i) ? "idle" : stripSpeed(i.speed || 0)),
      busy.has(i.id) ? el("span", { class: "busy" }, "busy") : null);
  }));
  fitStrip();
}

// fitStrip gives the window's strip the short names when the full ones
// don't fit on the header's line, and only the colours and speeds when the
// short ones don't either (each still says its name on hover).
function fitStrip() {
  const strip = $("strip");
  if (!document.body.classList.contains("windowed")) return;
  // The chips reach 4px past the strip by design (their hover box); that is not running out of room.
  const over = () => strip.scrollWidth > strip.clientWidth + 5;
  strip.classList.remove("tight", "tighter");
  if (over()) strip.classList.add("tight");
  if (over()) strip.classList.add("tighter");
}
addEventListener("resize", fitStrip);

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
function openAdd(on) {
  $("addBox").hidden = !on;
  $("toggleAdd").setAttribute("aria-expanded", String(on));
  if (on) $("addUrl").focus();
  else $("toggleAdd").focus();
}
$("toggleAdd").onclick = () => openAdd($("addBox").hidden);
$("addBox").addEventListener("keydown", (e) => {
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
  $("addPage").hidden = !v.page;
  $("addVpn").hidden = !v.vpn;
  $("addGo").textContent = v.label;
  $("addUrl").rows = Math.min(5, Math.max(2, $("addUrl").value.split(/\n/).length)); // grows with the links, to five lines
  $("addHint").textContent = v.bad ? `${plural(v.bad, "line")} that isn't a link will stay in the box.` : v.count ? "" : "Paste links here, or anywhere on the list with Ctrl+V.";
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
  $("addUrl").dispatchEvent(new Event("input"));
  openAdd(false);
});

// The list's sections (lib/rows.js): a heading with how many it
// holds, which folds its rows away. Folded ones are remembered in this browser.
const FOLDED = "dms.foldedSections";
const folded = new Set((() => { try { return JSON.parse(localStorage.getItem(FOLDED)) || []; } catch { return []; } })());
const heads = new Map(); // section name -> { root, count, button }
function sectionHead(name) {
  let h = heads.get(name);
  if (!h) {
    const button = el("button", { onclick: () => {
      folded.has(name) ? folded.delete(name) : folded.add(name);
      try { localStorage.setItem(FOLDED, JSON.stringify([...folded])); } catch { /* until the page closes */ }
      renderDownloads(latest.state);
    } });
    button.className = name === "Needs you" ? "mk bad" : "mk";
    h = { button, count: el("span", { class: "count num" }) };
    button.append(name, h.count, icon("chevron"));
    h.root = el("h2", { class: "sec" }, button,
      name === "Finished" ? el("a", { href: "#", onclick: (e) => { e.preventDefault(); clearFinished(); } }, "Clear finished") : null);
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
  const done = (latest.state.downloads || []).filter(finished);
  await Promise.all(done.map((d) => call("remove", { id: d.id, deleteFile: false }).catch(() => {})));
  toast(done.length === 1 ? "1 finished download cleared (files kept)" : `${done.length} finished downloads cleared (files kept)`);
}

const groupCards = new Map(); // group id (a Metalink or a split archive) -> card controller

// The search box narrows the list as it is typed.
$("search").addEventListener("input", () => { if (latest.state) renderDownloads(latest.state); });

let announced = null; // id -> status last seen, for the screen reader's news

function renderDownloads(state) {
  const list = $("list");
  const all = state.downloads || [];
  const news = announcements(announced, all);
  announced = news.seen;
  if (news.texts.length) $("announce").textContent = news.texts.join(". ");
  const query = $("search").value;
  const res = listRows(state, { query, folded, selected, opened: detail.id });
  const seen = new Set();
  const seenGroups = new Set();
  for (const id of selected) if (!res.selected.has(id)) selected.delete(id);
  if (res.openedGone) {
    detail.id = "";
    $("detail").hidden = true;
    document.body.classList.remove("detailing");
  }
  const want = []; // the list's nodes, in order
  shown = res.shown;
  for (const s of res.sections) {
    const head = sectionHead(s.name);
    head.count.textContent = s.count;
    want.push(head.root);
    for (const row of s.rows) {
      let c;
      if (row.d) {
        seen.add(row.d.id);
        c = cardFor(row.d.id);
        c.update(row.d, row.form);
      } else {
        seenGroups.add(row.group);
        for (const i of row.items) seen.add(i.d.id);
        c = groupCards.get(row.group);
        if (!c) groupCards.set(row.group, c = makeGroupCard(row.group));
        c.update(row);
      }
      c.number(row.place);
      if (!s.folded) want.push(c.root);
    }
  }
  for (const id of cards.keys()) if (!seen.has(id)) cards.delete(id);
  for (const id of groupCards.keys()) if (!seenGroups.has(id)) groupCards.delete(id);
  renderSelBar();
  const emptyText = `No downloads match “${query.trim()}”.`;
  if (!all.length) want.push(welcome(state));
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
function welcome(state) {
  const ready = (state.interfaces || []).filter((i) => !i.tunnel && i.present !== false && i.enabled);
  const sig = ready.map((i) => i.id).join();
  if (welcomeNode && sig === welcomeSig) return welcomeNode;
  welcomeSig = sig;
  const f = field(false);
  // The connections' colours in a few squares: they are what fills a file.
  [3, 4, 20, 19, 35].forEach((k, n) => { if (ready.length) f.children[k].style.background = ifaceHue(ready[n % ready.length].id, state); });
  const feat = (name, text) => [el("span", { class: "st-tile seed" }, icon(name)), el("span", {}, text)];
  welcomeNode = el("div", { class: "void", role: "status" }, f,
    el("div", { class: "big" }, ready.length > 1 ? `${ready.length} connections ready to fill one file together.` : "Ready for your first download."),
    el("div", { class: "go" }, el("button", { class: "primary", onclick: () => openAdd(true) }, "+ Add a link"),
      el("span", { class: "muted" }, "or ", el("kbd", {}, "Ctrl"), "+", el("kbd", {}, "V"), " anywhere here")),
    el("div", { class: "feats" },
      feat("down", "Firefox downloads over 100 MB come here by themselves"),
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

// A newer engine release, as the engine found it on GitHub (at most once a
// day). Dismissed, it stays away until the next release; the choice is kept
// in this browser only.
const DISMISSED_UPDATE = "dms.dismissedUpdate";
function dismissedUpdate() {
  try { return localStorage.getItem(DISMISSED_UPDATE) || ""; } catch { return ""; }
}
function renderUpdate(u) {
  const note = $("updateNote");
  const show = !!(u && u.version && u.version !== dismissedUpdate());
  note.hidden = !show;
  if (!show || note.dataset.version === u.version) return;
  note.dataset.version = u.version;
  note.replaceChildren(
    el("span", {}, `DMS² v${u.version} is available: `, link(u.url, "see the release")),
    iconButton("close", "Dismiss this update notice", () => {
      try { localStorage.setItem(DISMISSED_UPDATE, u.version); } catch { /* shown again next time */ }
      note.hidden = true;
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
  renderUpdate(m.connected && m.state && m.state.update);
  // Connected, yet with an error: what is shown is out of date (see TOO_LONG in background/engine.js).
  $("staleWarn").hidden = !(m.connected && m.error);
  $("staleWarn").textContent = (m.connected && m.error) || "";
  document.querySelector(".tools").hidden = !m.connected || !m.state;
  if (!m.connected || !m.state) $("addBox").hidden = true;
  if (!m.connected || !m.state) {
    $("list").replaceChildren(m.notInstalled
      ? el("div", { class: "offline" }, field(true),
        el("div", { class: "big" }, "The download engine isn't installed."),
        el("div", {}, "DMS² needs its engine on this PC. Download the zip from the ", link(RELEASES_URL, "release page"),
          ", extract it and run dms-engine.exe."))
      : m.quit && Host.startEngine
      ? el("div", { class: "offline calm" }, field(true),
        el("div", { class: "big" }, "DMS² was quit from its tray icon."),
        el("div", {}, "Firefox handles downloads itself until it runs again."),
        el("div", { class: "actions" }, el("button", { class: "primary", onclick: () => Host.startEngine() }, "Start DMS²")))
      : el("div", { class: "offline", role: "alert" }, field(true),
        el("div", { class: "big" }, "The download engine isn't reachable."),
        el("div", {}, "Make sure DMS² is running in the tray, or run dms-engine.exe from the release zip again."),
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
  const s = m.state;
  fresh($("total"), s.active ? [fmtSpeed(s.speed), fmtETA(s.eta)] : [], () => s.active
    ? [el("b", {}, fmtSpeed(s.speed)), el("small", {}, `${fmtETA(s.eta)} left`)]
    : [el("small", {}, "Idle")]);
  const bulk = bulkActions(s.downloads || []);
  $("pauseAll").hidden = !bulk.pauseAll;
  $("resumeAll").hidden = !bulk.resumeAll;
  renderStrip(s);
  renderDownloads(s);
}
