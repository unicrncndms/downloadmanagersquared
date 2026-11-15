"use strict";

// What the downloads list draws, worked out from the engine's state and what
// the page itself holds (search box, folded sections, selection, the download
// opened in Details). Pure, no browser.* or DOM: the pages load it as a script
// after list.js and the Node tests require it.

const listLib = typeof module !== "undefined" ? require("./list.js")
  : { group, matchesSearch, parseQuery, SECTIONS, SECTION_KEYS };

// Newest first within a section, except the queued: those in the order they will start.
function sortDownloads(downloads) {
  return downloads.map((d, i) => ({ d, i }))
    .sort((a, b) => listLib.group(a.d) - listLib.group(b.d) || (listLib.group(a.d) === listLib.SECTION_KEYS.indexOf("queued") ? a.i - b.i : b.i - a.i))
    .map((x) => x.d);
}

// The order of a group's files or parts under its row: the engine's
// (summary.order), so a file it doesn't list comes last.
function groupOrder(items, summary) {
  const at = new Map(summary.order.map((id, i) => [id, i]));
  const place = (d) => (at.has(d.id) ? at.get(d.id) : Infinity);
  return [...items].sort((a, b) => place(a) - place(b));
}

// A download on its own, or the files of one Metalink or the parts of one
// split archive together as a group, where the first of them would be. A
// group the engine sends no summary for is plain rows; one down to one file
// shows that file on its own.
function listEntries(sorted, groups) {
  const summaries = new Map((groups || []).map((g) => [g.id, g]));
  const out = [];
  const seen = new Map();
  for (const d of sorted) {
    const summary = d.group && summaries.get(d.group);
    if (!summary) { out.push({ d }); continue; }
    let g = seen.get(d.group);
    if (!g) {
      g = { group: d.group, summary, items: [] };
      seen.set(d.group, g);
      out.push(g);
    }
    g.items.push(d);
  }
  return out.map((e) => (e.items && e.items.length === 1 ? { d: e.items[0] }
    : e.items ? { ...e, items: groupOrder(e.items, e.summary) } : e));
}

// A row's form: the whole card while its Details is open; a group's fetched
// part (waiting only for its archive's extraction) or a finished download as
// one line; else the card.
function formOf(d, opened, inGroup) {
  if (d.id === opened) return "card";
  if (inGroup && d.status === "completed") return "part";
  return d.section === "done" ? "line" : "card";
}

// A queued download nobody has touched needs no card (about a hundred
// elements): its place, name and size are all it shows. Anything else it could
// show (an error, bytes fetched, links, a VPN, a folder step, a key to trust)
// makes it a card.
function isLight(d) {
  return d.status === "queued" && !d.done && !d.error && !d.vpn && !d.folder && !d.hostKey && !Object.keys(d.links || {}).length;
}

// queueSlots: where each queued download stands in the whole queue and how
// long it is, so a card knows if it can move up or down without scanning.
function queueSlots(downloads) {
  const at = new Map();
  for (const d of downloads) if (d.status === "queued") at.set(d.id, at.size);
  return { at, len: at.size };
}

// listRows is the list as it is drawn. state is the engine's state with the
// clicks still on their way laid over it (predict); ui the page's own:
// { query, folded: Set of heading names, selected: Set of ids, opened: id }.
// Returns
//   sections: [{ name, count, folded, rows }], in the order listed, each row
//     { d, form, place } (one download) or { group, summary, items: [{ d,
//     form }], place } (a group); form is "card", "light" (a card not yet
//     worth building, see isLight), "line" or, in a group, "part";
//     place is the number in the Queue section, else 0. A folded section still
//     has its rows (it is only not drawn).
//   shown: the ids of each row not folded away, in order, for Shift-click ranges
//   selected: ui.selected without the downloads that are gone
//   openedGone: the opened download is gone
//   matched: how many downloads the search leaves
function listRows(state, ui) {
  const all = (state && state.downloads) || [];
  const query = listLib.parseQuery(ui.query);
  const matched = sortDownloads(all.filter((d) => listLib.matchesSearch(d, query)));
  const ids = new Set(all.map((d) => d.id));
  const sections = [], shown = [];
  let cur = null;
  for (const e of listEntries(matched, state && state.groups)) {
    const key = listLib.group(e.d || e.items[0]);
    const name = listLib.SECTIONS[listLib.SECTION_KEYS[key]];
    if (!cur || cur.name !== name) sections.push(cur = { name, count: 0, folded: ui.folded.has(name), rows: [] });
    cur.count++;
    const place = name === "Queue" ? cur.count : 0;
    const form = e.d && formOf(e.d, ui.opened, false);
    const row = e.d ? { d: e.d, form: form === "card" && isLight(e.d) && e.d.id !== ui.opened && !ui.selected.has(e.d.id) ? "light" : form, place }
      : { group: e.group, summary: e.summary, items: e.items.map((d) => ({ d, form: formOf(d, ui.opened, true) })), place };
    cur.rows.push(row);
    if (!cur.folded) shown.push(e.d ? [e.d.id] : e.items.map((d) => d.id));
  }
  return {
    sections, shown, matched: matched.length,
    selected: new Set([...ui.selected].filter((id) => ids.has(id))),
    openedGone: !!ui.opened && !ids.has(ui.opened),
    queue: queueSlots(all), // of all the downloads, not only those the search leaves
  };
}

if (typeof module !== "undefined") module.exports = { listRows, isLight, queueSlots };
