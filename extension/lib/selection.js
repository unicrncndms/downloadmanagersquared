"use strict";

// Which downloads the list has selected, and a removal that waits for Undo.
// Pure, no browser.* or DOM: popup.js loads it as a script and the Node tests
// require it.

// clickRows applies a click on a row (ids: the row's downloads, several for a
// group card) to selected, a Set it changes, and returns the new anchor, where
// a Shift click's range starts. mods are the click's shiftKey, ctrlKey and
// metaKey; order is the ids of each row in the list, in order.
// - plain: only this row, or nothing when it was already the whole selection
// - Ctrl (Cmd): this row added, or taken out when all of it was in
// - Shift: every row from the anchor to this one added; the anchor stays
function clickRows(selected, anchor, ids, mods, order) {
  if (mods.shiftKey && anchor) {
    const flat = order.flat();
    const [a, b] = [flat.indexOf(anchor), flat.indexOf(ids[0])].sort((x, y) => x - y);
    if (a >= 0) for (const id of flat.slice(a, b + 1)) selected.add(id);
    for (const id of ids) selected.add(id);
    return anchor;
  }
  if (mods.ctrlKey || mods.metaKey) {
    const on = ids.every((id) => selected.has(id));
    for (const id of ids) on ? selected.delete(id) : selected.add(id);
  } else {
    const only = selected.size === ids.length && ids.every((id) => selected.has(id));
    selected.clear();
    if (!only) for (const id of ids) selected.add(id);
  }
  return ids[0];
}

// undoRemoval holds one removal at a time until its Undo note ends: start
// hides the rows (hiding, a Set of ids the list leaves out) and, after ms,
// calls remove(ids). Undo shows them again and remove is never called; a new
// start, or flush (the page closing), sends the waiting one at once. The rows
// stay hidden until remove settles, so they don't flash back before the
// engine's state has them gone, and come back if it fails. onEnd runs once
// when the removal is sent or undone (to take its note away). timers is
// globalThis, or a fake clock in tests.
function undoRemoval(remove, timers = globalThis) {
  const hiding = new Set();
  let waiting = null; // { ids, timer, onEnd }
  function end() {
    const w = waiting;
    waiting = null;
    timers.clearTimeout(w.timer);
    if (w.onEnd) w.onEnd();
    return w.ids;
  }
  function flush() {
    if (!waiting) return undefined;
    const ids = end();
    return new Promise((ok) => ok(remove(ids))) // called now, a throw becomes a rejection
      .finally(() => { for (const id of ids) hiding.delete(id); });
  }
  return {
    hiding,
    flush,
    start(ids, ms, onEnd) {
      flush();
      for (const id of ids) hiding.add(id);
      waiting = { ids, onEnd, timer: timers.setTimeout(flush, ms) };
    },
    undo() {
      if (!waiting) return;
      for (const id of end()) hiding.delete(id);
    },
  };
}

if (typeof module !== "undefined") module.exports = { clickRows, undoRemoval };
