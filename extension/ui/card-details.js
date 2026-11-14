"use strict";

// The parts of a download's Details that load or change on their own, taken
// out of card.js makeCard: a torrent's files, the activity log, and the
// Source and Checksum forms. Each takes the download's id and, where it reads
// the download, getD: a function giving it as last shown.

// A section of Details: its heading, then what it holds.
const detailSection = (title, ...body) => el("section", {}, el("h3", { class: "mk" }, title), ...body);

// torrentFilesPart is the files of a torrent: tick or untick what to
// download, with the progress of each. load() asks for the list; it is called
// while Details is open, like the activity.
function torrentFilesPart(id, getD) {
  const tfList = el("div", { class: "files" });
  const tfNote = el("p", { class: "muted", style: "margin: 0 0 var(--s2)" });
  const section = detailSection("Files", tfNote, tfList);
  section.hidden = true;
  let tfAt = 0, tfSig = "", tfRows = [];
  async function load() {
    if (Date.now() - tfAt < 2000) return;
    tfAt = Date.now();
    const d = getD();
    let files = [];
    try { files = await call("torrentFiles", { id }); } catch { return; }
    section.hidden = !files.length;
    const sig = JSON.stringify(files.map((f) => [f.path, f.size, f.selected]));
    const canEdit = d.status !== "seeding" && !finished(d); // a finished torrent keeps what it has
    if (sig !== tfSig) {
      tfSig = sig;
      const boxes = files.map((f) => el("input", { type: "checkbox", checked: f.selected, disabled: !canEdit }));
      boxes.forEach((b) => b.addEventListener("change", async () => {
        try { await call("setTorrentFiles", { id, files: boxes.map((x) => x.checked) }); } catch (e) { b.checked = !b.checked; toast(e.message, true); }
        tfAt = 0;
      }));
      tfRows = files.map((f, i) => el("span", { class: "muted num" }));
      tfList.replaceChildren(...files.map((f, i) => el("label", {}, boxes[i], el("span", { class: "path" }, f.path), tfRows[i])));
    }
    files.forEach((f, i) => { tfRows[i].textContent = `${fmtBytes(f.size)}${f.selected && f.size > 0 ? ` · ${Math.round(((f.done || 0) / f.size) * 100)}%` : ""}`; });
    tfNote.textContent = canEdit ? "Tick a file to download it, untick to stop fetching it." : "";
  }
  return { section, load };
}

// activityPart is the engine's log of this download, newest first, asked for
// while Details is open (it stays out of the state sent every second).
// reset() makes the next load ask again; say(text) shows a note in its place.
function activityPart(id) {
  const activityList = el("ol", { class: "activity num" });
  let activity = [];
  let activityAt = 0;
  const copy = el("button", { class: "small", onclick: async () => {
    try {
      await navigator.clipboard.writeText(activity.join("\n"));
      toast("Activity copied");
    } catch {
      toast("Couldn't copy the activity", true);
    }
  } }, "Copy");
  const section = detailSection("Activity", activityList, copy);
  section.hidden = true; // until the log comes
  async function load() {
    if (Date.now() - activityAt < 2000) return;
    activityAt = Date.now();
    let events = [];
    try { events = await call("downloadLog", { id }); } catch { return; }
    activity = logLines(events, (ifc) => ifaceName(ifc, latest.state, latest.settings));
    copy.hidden = false;
    // The time leads each line, quietly: it is the first 19 characters (lib/list.js logLines).
    activityList.replaceChildren(...activity.map((line) => el("li", {}, el("span", { class: "muted" }, line.slice(0, 19)), line.slice(19))));
    section.hidden = !activity.length;
  }
  function say(text) {
    section.hidden = false;
    activityList.replaceChildren(el("li", { class: "muted" }, text));
    copy.hidden = true;
  }
  return { section, load, say, reset() { activityAt = 0; } };
}

// sourceSumPart is Source (the link and the landing page) and Checksum
// (SHA-256, MD5): values, and Edit for boxes. update(d, torrent, done) draws
// them; a torrent has neither, and a finished file shows only its link and a
// checksum it was given.
function sourceSumPart(id, getD) {
  const sourceFacts = el("dl", { class: "facts" });
  const page = el("input", { type: "url", placeholder: "The page you started from, or a fresh link to the file" });
  const sha = el("input", { type: "text", class: "mono", placeholder: "64 hex characters (optional)", spellcheck: "false" });
  const md5 = el("input", { type: "text", class: "mono", placeholder: "32 hex characters (optional)", spellcheck: "false" });
  const sourceForm = el("div", { class: "form", hidden: true },
    el("label", {}, "Landing page"), page,
    el("div", { class: "actions" },
      el("button", { class: "small primary", onclick: saveSource }, "Save"),
      el("button", { class: "small", onclick: () => editSource(false) }, "Cancel")));
  const sourceEdit = el("button", { class: "small", onclick: () => editSource(true) }, "Edit");
  const sourceSection = detailSection("Source", sourceFacts, sourceEdit, sourceForm);
  const sumFacts = el("dl", { class: "facts" });
  const sumForm = el("div", { class: "form", hidden: true },
    el("label", {}, "SHA-256"), sha,
    el("label", {}, "MD5"), md5,
    el("div", { class: "actions" },
      el("button", { class: "small primary", onclick: saveSums }, "Save"),
      el("button", { class: "small", onclick: () => editSums(false) }, "Cancel")));
  const sumEdit = el("button", { class: "small", onclick: () => editSums(true) }, "Edit");
  const checkSection = detailSection("Checksum", sumFacts, sumEdit, sumForm);

  const editSource = editable(sourceFacts, sourceForm, sourceEdit, () => { page.value = getD().pageUrl || ""; });
  async function saveSource() {
    await act("setPageUrl", { id, pageUrl: page.value }, "Saved");
    editSource(false);
  }
  const editSums = editable(sumFacts, sumForm, sumEdit, () => {
    sha.value = getD().sha256 || "";
    md5.value = getD().md5 || "";
  });
  async function saveSums() {
    await act("setHashes", { id, sha256: sha.value, md5: md5.value }, "Saved");
    editSums(false);
  }

  function update(d, torrent, done) {
    sourceSection.hidden = checkSection.hidden = torrent;
    if (!torrent && sourceForm.hidden) {
      // A finished file has no use for its landing page: only the link, to copy or open.
      const src = done ? [["Link", d.url]] : [["Link", d.url], ["Landing page", d.pageUrl]];
      sourceEdit.hidden = done;
      fresh(sourceFacts, src, () => src.flatMap(([k, v]) =>
        [el("dt", {}, k), v ? linkCell(v, k === "Link" ? "link" : "page", /^https?:/i.test(v) ? () => Host.openUrl(v) : null) : el("dd", { class: "muted" }, "none")]));
    }
    if (!torrent && sumForm.hidden) {
      // Finished: only a checksum that was given, with what checking it found; else just a way to give one.
      const given = d.sha256 || d.md5;
      const sums = done ? (given ? [["SHA-256", d.sha256], ["MD5", d.md5]].filter(([, v]) => v).concat([["Result", d.hashResult === "match" ? "Verified" : d.hashResult ? "Does not match: " + d.hashResult : "Not checked yet"]]) : [])
        : [["SHA-256", d.sha256], ["MD5", d.md5]];
      sumEdit.textContent = done && !given ? "Check against a checksum…" : "Edit";
      fresh(sumFacts, sums, () => sums.flatMap(([k, v]) =>
        [el("dt", {}, k), el("dd", !v ? { class: "muted" } : { class: "mono" }, v || "none")]));
    }
  }

  return { sourceSection, checkSection, page, editSource, update };
}
