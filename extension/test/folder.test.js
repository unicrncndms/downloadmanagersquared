"use strict";

const test = require("node:test");
const assert = require("node:assert");
const { folderView, removeWording, addFolderChoice } = require("../lib/folder.js");

const SETTINGS = { saveDir: "C:\\Users\\you\\Downloads", folderDefault: false };
const zip = (folder, extra) => ({ kind: "http", id: "z", filename: "big.zip", status: "running", folder, ...extra });

test("no toggle for a torrent, a download that isn't a .zip, or a finished one", () => {
  assert.equal(folderView({ kind: "torrent", folder: { on: false } }, SETTINGS), null);
  assert.equal(folderView({ kind: "http", filename: "big.iso" }, SETTINGS), null);
  const done = folderView(zip({ on: true, into: "D:\\x", out: "D:\\x\\big" }, { status: "completed" }), SETTINGS);
  assert.equal(done.toggle, null);
  assert.deepEqual(done.facts[0], ["Extracted to", "D:\\x\\big"]);
});

test("a .zip shows the toggle, off and free to switch on", () => {
  const v = folderView(zip({ on: false }), SETTINGS);
  assert.deepEqual(v.toggle, { on: false, disabled: false, reason: "" });
  assert.equal(v.ask, null);
});

test("switched on by hand without a folder: the add step, prefilled with the last folder", () => {
  assert.deepEqual(folderView(zip({ on: true }), SETTINGS).ask, { prefill: SETTINGS.saveDir });
  const last = { ...SETTINGS, folderDir: "E:\\Extracted" };
  assert.deepEqual(folderView(zip({ on: true }), last).ask, { prefill: "E:\\Extracted" });
  assert.equal(folderView(zip({ on: true, into: "E:\\Extracted" }), last).ask, null);
});

test("once locked, the switch goes and it just says it extracts", () => {
  const v = folderView(zip({ on: true, into: "D:\\x", locked: true, why: "Some files are already extracted and their space freed" }), SETTINGS);
  assert.equal(v.toggle, null);
  assert.equal(v.extracting, true);
  assert.equal(folderView(zip({ on: true, into: "D:\\x" }), SETTINGS).extracting, false);
});

test("unavailable, with the engine's reason: a checksum, no ranges, not a .zip", () => {
  for (const why of ["A checksum is entered", "The server doesn't allow split downloading", "Only a .zip can be downloaded as a folder"]) {
    const t = folderView(zip({ on: false, why }), SETTINGS).toggle;
    assert.deepEqual(t, { on: false, disabled: true, reason: why });
  }
});

test("Details lists the folder, the fallback reason, renamed Entries and a skipped digest", () => {
  const v = folderView(zip({ on: true, into: "D:\\x", out: "D:\\x\\big", note: "The server's checksum wasn't checked", renamed: ["CON.txt → _CON.txt", "a?.txt → a_.txt"] }), SETTINGS);
  assert.deepEqual(v.facts, [
    ["Folder", "D:\\x\\big"],
    ["Note", "The server's checksum wasn't checked"],
    ["Renamed", "CON.txt → _CON.txt\na?.txt → a_.txt"],
  ]);
  const fell = folderView(zip({ on: false, note: "Downloaded as a file: not a ZIP" }), SETTINGS);
  assert.deepEqual(fell.facts, [["Note", "Downloaded as a file: not a ZIP"]]);
});

test("Remove says folder for a Download as folder", () => {
  const file = removeWording(zip(undefined));
  assert.equal(file.del, "Delete partial file");
  const asFile = removeWording(zip({ on: false }));
  assert.equal(asFile.del, "Delete partial file");
  const folder = removeWording(zip({ on: true, into: "D:\\x" }));
  assert.match(folder.del, /folder/);
  assert.match(folder.keep, /folder/);
  assert.match(folder.deleted, /folder/);
});

test("the add form offers the switch for a .zip link, ticked as the settings default", () => {
  assert.equal(addFolderChoice("https://example.com/file/abc", SETTINGS).show, false);
  assert.deepEqual(addFolderChoice("https://example.com/big.ZIP?x=1", SETTINGS), { show: true, checked: false });
  assert.deepEqual(addFolderChoice("https://example.com/big.zip", { ...SETTINGS, folderDefault: true }), { show: true, checked: true });
  assert.equal(addFolderChoice("not a url", SETTINGS).show, false);
});

test("the add form sends the switch only when it differs from the default", () => {
  const { addFolderParam } = require("../lib/folder.js");
  assert.equal(addFolderParam(false, true, SETTINGS), undefined); // not shown
  assert.equal(addFolderParam(true, false, SETTINGS), undefined); // as the default
  assert.equal(addFolderParam(true, true, SETTINGS), true); // on by hand: asks for the folder
  assert.equal(addFolderParam(true, false, { ...SETTINGS, folderDefault: true }), false);
  assert.equal(addFolderParam(true, true, { ...SETTINGS, folderDefault: true }), undefined);
});

test("while unfinished, Extract into can be changed, with the move's warning and why one failed", () => {
  const v = folderView(zip({ on: true, into: "D:\\x", out: "D:\\x\\big", moveWarning: "Moving now re-downloads about 3.2 GiB" }), SETTINGS);
  assert.deepEqual(v.move, { into: "D:\\x", moving: "", warning: "Moving now re-downloads about 3.2 GiB", error: "" });
  const failed = folderView(zip({ on: true, into: "D:\\x", out: "D:\\x\\big", moveError: "Couldn't move: a file in it is in use" }), SETTINGS);
  assert.equal(failed.move.error, "Couldn't move: a file in it is in use");
  const pending = folderView(zip({ on: true, into: "D:\\x", out: "D:\\x\\big", moving: "E:\\y" }, { status: "paused" }), SETTINGS);
  assert.equal(pending.move.moving, "E:\\y");
  assert.equal(folderView(zip({ on: true }), SETTINGS).move, null); // still asking
  assert.equal(folderView(zip({ on: true, into: "D:\\x", out: "D:\\x\\big" }, { status: "completed" }), SETTINGS).move, null);
});

test("Change… offers the recent folders, then the Save folder, without the one it's in", () => {
  const { folderChoices } = require("../lib/folder.js");
  const s = { saveDir: "C:\Downloads", folderDirs: ["E:\Data", "D:\Games", "c:\downloads"] };
  assert.deepEqual(folderChoices(s, ""), ["E:\Data", "D:\Games", "c:\downloads"]);
  assert.deepEqual(folderChoices(s, "e:\data"), ["D:\Games", "c:\downloads"]);
  assert.deepEqual(folderChoices({ saveDir: "C:\Downloads" }, ""), ["C:\Downloads"]);
  assert.deepEqual(folderChoices({ saveDir: "C:\Downloads", folderDirs: ["E:\Data"] }, ""), ["E:\Data", "C:\Downloads"]);
  assert.deepEqual(folderChoices(null, ""), []);
});
