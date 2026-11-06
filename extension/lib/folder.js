"use strict";

// What the downloads page shows of Download as folder. Pure, no browser.* or
// DOM: the popup loads it as a script and the Node tests require it. d is a
// download from the engine's state, d.folder its FolderSummary (on, into, out,
// locked, files, of, renamed, note, why); settings the engine's settings. The
// reasons are the engine's (ADR 0003): why says why the switch can't change.

// folderView is the toggle, the add step and the facts for Details, or null
// when the download has nothing to do with it (a torrent, a name that isn't
// .zip and never asked for it).
//   toggle: { on, disabled, reason } while unfinished, else null
//   ask:    { prefill } while it waits for a folder to extract into, else null
//   facts:  [label, value] pairs
function folderView(d, settings) {
  const f = d && d.kind !== "torrent" ? d.folder : null;
  if (!f) return null;
  const done = d.status === "completed";
  let toggle = null;
  if (!done) {
    const reason = f.why || "";
    toggle = { on: !!f.on, disabled: !!reason || !!f.locked, reason };
  }
  const ask = f.on && !f.into && !done ? { prefill: lastFolder(settings) } : null;
  const facts = [];
  if (f.on && f.into && !(done && f.out)) facts.push(["Extract into", f.into]);
  if (f.on && f.out) facts.push([done ? "Extracted to" : "Folder", f.out]);
  if (f.note) facts.push(["Note", f.note]);
  if (f.renamed && f.renamed.length) facts.push(["Renamed", f.renamed.join("\n")]);
  return { toggle, ask, facts };
}

// lastFolder is where a Download as folder extracts unless told otherwise:
// the last folder used for one, else the Save folder (as the engine decides).
function lastFolder(settings) {
  return (settings && (settings.folderDir || settings.saveDir)) || "";
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
// link whose path ends in .zip, ticked as the settings default. (A name
// learned only from the server is offered in Details instead.)
function addFolderChoice(url, settings) {
  let show = false;
  try {
    show = /\.zip$/i.test(new URL(url).pathname);
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

if (typeof module !== "undefined") module.exports = { folderView, removeWording, addFolderChoice, addFolderParam, lastFolder };
