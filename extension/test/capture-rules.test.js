"use strict";

const test = require("node:test");
const assert = require("node:assert");
const r = require("../lib/capture-rules.js");

const MB = 1e6;
const h = (o) => r.headerMap(Object.entries(o).map(([name, value]) => ({ name, value })));

test("headers are read case-insensitively", () => {
  const m = h({ "Content-Type": "Application/Octet-Stream; charset=x", "CONTENT-LENGTH": "5" });
  assert.equal(r.mediaType(m), "application/octet-stream");
  assert.equal(r.responseSize(m, 200), 5);
});

test("pages, images and PDFs stay in the browser; attachments and binaries don't", () => {
  assert.ok(r.isPage(h({ "content-type": "text/html; charset=utf-8" })));
  assert.ok(!r.looksLikeDownload(h({ "content-type": "image/png" })));
  assert.ok(!r.looksLikeDownload(h({ "content-type": "application/pdf" })));
  assert.ok(r.looksLikeDownload(h({ "content-type": "application/pdf", "content-disposition": "attachment" })));
  assert.ok(r.looksLikeDownload(h({ "content-type": "application/x-iso9660-image" })));
});

test("the full size comes from Content-Range on a partial response", () => {
  assert.equal(r.responseSize(h({ "content-range": "bytes 0-0/734003200", "content-length": "1" }), 206), 734003200);
  assert.equal(r.responseSize(h({}), 200), -1);
});

test("filenames come from Content-Disposition, RFC 5987 first", () => {
  assert.equal(r.dispositionName(h({ "content-disposition": "attachment; filename=\"a b.iso\"" })), "a b.iso");
  assert.equal(r.dispositionName(h({ "content-disposition": "attachment; filename=x.iso; filename*=UTF-8''%C3%BC.iso" })), "ü.iso");
  assert.equal(r.dispositionName(h({})), "");
});

test("a torrent file is recognised by type, name or path", () => {
  assert.ok(r.isTorrentFile(h({ "content-type": "application/x-bittorrent" }), "https://x/y"));
  assert.ok(r.isTorrentFile(h({ "content-disposition": "attachment; filename=\"a.torrent\"" }), "https://x/dl"));
  assert.ok(r.isTorrentFile(h({}), "https://x/a.TORRENT?k=1"));
  assert.ok(!r.isTorrentFile(h({}), "https://x/a.iso"));
});

test("a response is captured at the threshold, a torrent at any size", () => {
  const big = h({ "content-type": "application/octet-stream", "content-length": String(100 * MB) });
  assert.equal(r.captureResponse(big, "https://x/a.iso", 200, 100 * MB), "download");
  assert.equal(r.captureResponse(big, "https://x/a.iso", 200, 100 * MB + 1), "");
  assert.equal(r.captureResponse(big, "https://x/a.iso", 404, 1), "");
  assert.equal(r.captureResponse(h({ "content-type": "application/x-bittorrent", "content-length": "900" }), "https://x/t", 200, 100 * MB), "torrent");
  assert.equal(r.captureResponse(h({ "content-type": "text/html", "content-length": String(200 * MB) }), "https://x/a.torrent", 200, 1), "");
});

test("a browser download is captured by its size or as a torrent", () => {
  assert.equal(r.captureDownload({ totalBytes: 200 * MB }, 100 * MB), "download");
  assert.equal(r.captureDownload({ totalBytes: -1, fileSize: 200 * MB }, 100 * MB), "download");
  assert.equal(r.captureDownload({ totalBytes: 5 * MB }, 100 * MB), "");
  assert.equal(r.captureDownload({ totalBytes: 900, filename: "C:\\D\\a.torrent" }, 100 * MB), "torrent");
});

test("pages for a download: the tab's history, hints, then referers back", () => {
  const referers = new Map([["https://host/a", "https://search/q"], ["https://search/q", "https://host/a"]]);
  const pages = r.pagesFor(["https://host/a", "https://host/b"], referers, ["https://host/c", "https://host/b", ""], "https://cdn/file");
  assert.deepEqual(pages, ["https://search/q", "https://host/a", "https://host/b", "https://host/c"]);
  assert.deepEqual(r.pagesFor(undefined, new Map(), ["about:blank", "https://cdn/file"], "https://cdn/file"), []);
});

test("the landing page steps back on the same site only", () => {
  const pages = ["https://other.org/", "https://dl.host.com/file/1", "https://www.host.com/wait/1"];
  assert.equal(r.landingPage(pages, 0), "https://www.host.com/wait/1");
  assert.equal(r.landingPage(pages, 1), "https://dl.host.com/file/1");
  assert.equal(r.landingPage(pages, 5), "https://dl.host.com/file/1");
  assert.equal(r.landingPage([], 1), "");
});

test("an unknown host starts one page back; a learned host as many as it learned", () => {
  const pages = ["https://host.com/a", "https://host.com/b", "https://host.com/c"];
  assert.equal(r.stepBackFor({}, pages), 1);
  assert.equal(r.stepBackFor({ "host.com": 0 }, pages), 0);
  assert.equal(r.stepBackFor({ "host.com": 2 }, pages), 2);
  assert.equal(r.landingPage(pages, r.stepBackFor({ "host.com": 2 }, pages)), "https://host.com/a");
  assert.equal(r.landingPage(pages, r.stepBackFor({ "host.com": 0 }, pages)), "https://host.com/c");
});

test("recording learns how many pages back the first click's page was", () => {
  const pages = ["https://host.com/a", "https://host.com/b", "https://host.com/c"];
  assert.equal(r.learnedStepBack(pages, "https://host.com/c"), 0);
  assert.equal(r.learnedStepBack(pages, "https://host.com/a"), 2);
  assert.equal(r.learnedStepBack(pages, "https://elsewhere/"), null);
  assert.equal(r.learnedStepBack([], "https://host.com/a"), null);
});
