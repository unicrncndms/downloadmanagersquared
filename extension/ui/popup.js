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
// A .zip, .7z or .rar link offers Download as folder, ticked as the settings default.
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

// The heading over the finished lines, with Clear finished.
const finishedHead = el("div", { class: "group-head" },
  el("span", { class: "muted" }, "Finished"),
  el("a", { href: "#", onclick: (e) => { e.preventDefault(); clearFinished(); } }, "Clear finished"));

async function clearFinished() {
  const done = (latest.state.downloads || []).filter(finished);
  for (const d of done) await call("remove", { id: d.id, deleteFile: false }).catch(() => {});
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
  const downloads = sortDownloads(all.filter((d) => matchesSearch(d, query)));
  const seen = new Set();
  const seenGroups = new Set();
  // Like place, by walking along: list.children[i] counts from the start
  // each time the list changed, which made a first draw of thousands take seconds.
  let at = list.firstElementChild;
  const put = (node) => {
    if (at === node) at = at.nextElementSibling;
    else list.insertBefore(node, at);
  };
  let headed = false;
  for (const e of listEntries(downloads)) {
    const done = e.d ? finished(e.d) : e.items.every(finished);
    if (done && !headed) {
      put(finishedHead);
      headed = true;
    }
    if (e.d) {
      seen.add(e.d.id);
      const c = cardFor(e.d.id);
      c.update(e.d);
      put(c.root);
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
    put(g.root);
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
  const emptyText = !all.length ? "No downloads yet. Big downloads in Firefox are picked up automatically, or paste a link under + Add."
    : `No downloads match “${query.trim()}”.`;
  let empty = $("emptyList");
  if (!downloads.length && !empty) list.append(empty = el("div", { class: "empty", id: "emptyList", role: "status" }));
  if (!downloads.length && empty.textContent !== emptyText) empty.textContent = emptyText;
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

connectUI((m) => {
  latest = m;
  renderConn(m);
  renderVersion(m);
  renderUpdate(m.connected && m.state && m.state.update);
  // Connected, yet with an error: what is shown is out of date (see TOO_LONG in background/engine.js).
  $("staleWarn").hidden = !(m.connected && m.error);
  $("staleWarn").textContent = (m.connected && m.error) || "";
  if (!m.connected || !m.state) {
    $("list").replaceChildren(m.notInstalled
      ? el("div", { class: "offline" },
        el("b", {}, "Engine not installed. "),
        "DMS² needs its download engine on this PC. Download the zip from the ",
        link(RELEASES_URL, "release page"),
        ", extract it and run dms-engine.exe.")
      : m.quit && Host.startEngine
      ? el("div", { class: "offline" },
        "DMS² was quit from its tray icon, so Firefox handles downloads itself. ",
        el("button", { onclick: () => Host.startEngine() }, "Start DMS²"))
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
