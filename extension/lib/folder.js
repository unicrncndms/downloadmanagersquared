"use strict";

// What the downloads page shows of Download as folder. Pure, no browser.* or
// DOM: the popup loads it as a script and the Node tests require it. d is a
// download from the engine's state, d.folder its FolderSummary (on, into, out,
// locked, files, of, renamed, note, why, moving, moveError, moveWarning);
// settings the engine's settings. The reasons are the engine's (ADR 0003):
// why says why the switch can't change.

// folderView is the toggle, the add step and the facts for Details, or null
// when the download has nothing to do with it (a torrent, a name that isn't
// .zip, .7z or .rar and never asked for it).
//   toggle: { on, disabled, reason } while unfinished and not locked, else null
//   extracting: locked (Entries freed), so Details just says it extracts
//   ask:   { prefill } while it waits for a folder to extract into, else null
//   password: { wrong } while an encrypted archive waits for its password
//           (wrong: the last one given didn't open it), else null
//   move:   { into, moving, warning, error } once it has one, while unfinished:
//           "Extract into" with Browse… to move it, else null
//   facts:  [label, value] pairs
function folderView(d, settings) {
  const f = d && d.kind !== "torrent" ? d.folder : null;
  if (!f) return null;
  const done = d.status === "completed";
  let toggle = null;
  if (!done && !f.locked) {
    const reason = f.why || "";
    toggle = { on: !!f.on, disabled: !!reason, reason };
  }
  const ask = f.on && !f.into && !done ? { prefill: lastFolder(settings) } : null;
  const move = f.on && f.into && !done
    ? { into: f.into, moving: f.moving || "", warning: f.moveWarning || "", error: f.moveError || "" }
    : null;
  const facts = [];
  if (f.on && f.into && done && !f.out) facts.push(["Extract into", f.into]);
  if (f.on && f.out) facts.push([done ? "Extracted to" : "Folder", f.out]);
  if (f.note) facts.push(["Note", f.note]);
  if (f.renamed && f.renamed.length) facts.push(["Renamed", f.renamed.join("\n")]);
  const password = f.on && f.ask && !done ? { wrong: f.ask === "wrong" } : null;
  return { toggle, extracting: !done && !!f.locked, ask, password, move, facts };
}

// setFolderView is folderView for a split archive's parts as one, for their
// group card: the switch and the add step, and id, the part to ask the engine
// with (it sets every part); password as folderView's, with the id of the
// part that asks for it (part 1, which may be complete: a part is once it is
// fetched). null when there is nothing to show; no switch once any part is locked.
function setFolderView(items, settings) {
  const lead = items.find((d) => d.folder && d.status !== "completed");
  const asks = items.find((d) => d.folder && d.folder.on && d.folder.ask);
  if (!lead && !asks) return null;
  const v = lead ? folderView(lead, settings) : { toggle: null, ask: null };
  const locked = items.some((d) => d.folder && d.folder.locked);
  return {
    id: (lead || asks).id, toggle: locked ? null : v.toggle, ask: v.ask,
    password: asks ? { wrong: asks.folder.ask === "wrong", id: asks.id } : null,
  };
}

// lastFolder is where a Download as folder extracts unless told otherwise:
// the last folder used for one, else the Save folder (as the engine decides).
function lastFolder(settings) {
  return (settings && (settings.folderDir || settings.saveDir)) || "";
}

// folderChoices is what Change… offers inside the page, so the folder picker
// (which closes the toolbar popup) is needed only for another folder: the
// folders last extracted into, newest first, then the Save folder; each once,
// whatever its case, and not current, the folder it already extracts into.
function folderChoices(settings, current) {
  if (!settings) return [];
  const seen = new Set([(current || "").toLowerCase()]);
  const out = [];
  for (const d of [...(settings.folderDirs || []), settings.saveDir || ""]) {
    if (d && !seen.has(d.toLowerCase())) {
      seen.add(d.toLowerCase());
      out.push(d);
    }
  }
  return out;
}

// removeWording is what Remove asks for an unfinished download: a Download as
// folder deletes (or keeps) its folder along with the partial file.
function removeWording(d) {
  if (d && d.folder && d.folder.on) {
    return {
      del: "Delete folder and partial file", keep: "Keep folder and partial file",
      deleted: "Removed, folder and partial file deleted", kept: "Removed from list (folder and partial file kept)",
    };
  }
  return {
    del: "Delete partial file", keep: "Keep partial file",
    deleted: "Removed, partial file deleted", kept: "Removed from list (partial file kept)",
  };
}

// addFolderChoice is the add form's "Download as folder" box: offered for a
// link whose path ends in .zip, .7z or .rar, ticked as the settings default. (A name
// learned only from the server is offered in Details instead.)
function addFolderChoice(url, settings) {
  let show = false;
  try {
    show = /\.(zip|7z|rar)$/i.test(new URL(url).pathname);
  } catch { /* not a link yet */ }
  return { show, checked: !!(settings && settings.folderDefault) };
}

// addFolderParam is Add's folder switch: sent only when the box was shown and
// differs from the default, so leaving it alone keeps the default's folder
// (no question), and ticking it by hand asks where to extract.
function addFolderParam(shown, checked, settings) {
  if (!shown || checked === !!(settings && settings.folderDefault)) return undefined;
  return checked;
}

if (typeof module !== "undefined") module.exports = { folderView, setFolderView, folderChoices, removeWording, addFolderChoice, addFolderParam, lastFolder };
