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
//   keep:   keepView's archive keeping select while on and unfinished, else null
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
  const keep = f.on && !done ? keepView(f, !!(f.locked || f.keepFixed)) : null;
  return { toggle, extracting: !done && !!f.locked, ask, password, move, keep, facts };
}

// The ways to keep a Download as folder's archive, as the engine names them
// (ADR 0007), with what the select says of each.
const KEEP_CHOICES = [
  ["auto", "Automatic"],
  ["free", "Free while extracting"],
  ["untilExtracted", "Keep until extracted (interruption recovery)"],
  ["keep", "Keep the archive"],
];
const KEEP_HELP = "Interruption recovery: an update, reboot or crash while it extracts downloads nothing again, at the cost of room for the archive until it is extracted.";

// keepView is the archive keeping select: value, the choice (the setting's
// when none is made); disabled once bytes are freed (fixed), with why;
// help, the line under it; decided, what Automatic chose, "" until it has.
function keepView(f, fixed) {
  const value = f.keep || "auto";
  const label = (k) => (KEEP_CHOICES.find(([v]) => v === k) || [k, ""])[1];
  const decided = value === "auto" && f.keeping ? "Automatic chose: " + label(f.keeping).replace(/ \(.*\)$/, "").toLowerCase() : "";
  return {
    value, options: KEEP_CHOICES, disabled: fixed, help: KEEP_HELP, decided,
    reason: fixed ? "Some of it is already freed, so this can't change any more" : "",
  };
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
  const v = lead ? folderView(lead, settings) : { toggle: null, ask: null, keep: null };
  const locked = items.some((d) => d.folder && d.folder.locked);
  // Every part takes the Set's choice; it is fixed once any part is freed.
  const fixed = locked || items.some((d) => d.folder && d.folder.keepFixed);
  const keep = v.keep ? keepView(lead.folder, fixed) : null;
  return {
    id: (lead || asks).id, toggle: locked ? null : v.toggle, ask: v.ask, keep,
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

if (typeof module !== "undefined") module.exports = { folderView, setFolderView, keepView, folderChoices, removeWording, addFolderChoice, addFolderParam, lastFolder };
