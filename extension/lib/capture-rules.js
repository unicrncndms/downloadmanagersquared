"use strict";

// What DMS² takes over from the browser, and where a download's landing page
// is. Pure functions over plain data, no browser.*: the extension loads this
// as a background script and the Node tests require it.

// Lower-cased response headers as a plain object.
function headerMap(headers) {
  const h = {};
  for (const { name, value } of headers || []) h[name.toLowerCase()] = value || "";
  return h;
}

function mediaType(h) {
  return (h["content-type"] || "").split(";")[0].trim().toLowerCase();
}

function isPage(h) {
  const t = mediaType(h);
  return t === "text/html" || t === "application/xhtml+xml";
}

function isAttachment(h) {
  return /^\s*attachment/i.test(h["content-disposition"] || "");
}

// Something the browser would save rather than render.
function looksLikeDownload(h) {
  if (isAttachment(h)) return true;
  const t = mediaType(h);
  return !/^(text\/|image\/|application\/(xhtml\+xml|xml|json|javascript|pdf))/.test(t);
}

// A .torrent file: always DMS²'s, whatever its size.
function isTorrentFile(h, url) {
  if (mediaType(h) === "application/x-bittorrent") return true;
  if (/\.torrent$/i.test(dispositionName(h))) return true;
  try { return /\.torrent$/i.test(new URL(url).pathname); } catch (e) { return false; }
}

// Full file size from Content-Length, or from Content-Range on a 206.
function responseSize(h, status) {
  if (status === 206) {
    const m = /\/(\d+)\s*$/.exec(h["content-range"] || "");
    if (m) return Number(m[1]);
  }
  const n = Number(h["content-length"]);
  return Number.isFinite(n) && h["content-length"] !== undefined ? n : -1;
}

function dispositionName(h) {
  const cd = h["content-disposition"] || "";
  let m = /filename\*\s*=\s*[^']*'[^']*'([^;]+)/i.exec(cd);
  if (m) {
    try { return decodeURIComponent(m[1].trim()); } catch (e) { /* fall through */ }
  }
  m = /filename\s*=\s*"([^"]*)"/i.exec(cd) || /filename\s*=\s*([^;]+)/i.exec(cd);
  return m ? m[1].trim() : "";
}

function hostOf(url) {
  try { return new URL(url).hostname; } catch (e) { return ""; }
}

// Rough "same site" key: the last two labels of the host name.
function siteOf(url) {
  return hostOf(url).split(".").slice(-2).join(".");
}

function isHttp(url) {
  return /^https?:/.test(url || "");
}

// Whether a GET response is DMS²'s: "torrent" for a torrent file, "download"
// for a file at or above the threshold, "" to leave it to the browser.
function captureResponse(h, url, status, threshold) {
  if (isPage(h) || (status !== 200 && status !== 206)) return "";
  if (isTorrentFile(h, url)) return "torrent";
  return responseSize(h, status) >= threshold && looksLikeDownload(h) ? "download" : "";
}

// The same for a download the browser started without a response passing by
// (e.g. <a download>), from its downloads.DownloadItem.
function captureDownload(item, threshold) {
  const h = { "content-type": item.mime || "" };
  if (isTorrentFile(h, item.finalUrl || item.url) || /\.torrent$/i.test(item.filename || "")) return "torrent";
  if (isPage(h)) return "";
  const size = item.totalBytes > 0 ? item.totalBytes : item.fileSize;
  return size >= threshold ? "download" : "";
}

// Pages leading up to a download, oldest first, ending with the page that
// started it: the tab's history, then hints (e.g. the request's origin), then
// back through referers for pages from before the extension was watching.
// Never includes the download link itself.
function pagesFor(history, referers, hints, downloadUrl) {
  const pages = (history || []).slice();
  for (const u of hints) {
    if (u && !pages.includes(u)) pages.push(u);
  }
  for (let i = 0; i < 5 && pages.length; i++) {
    const ref = referers.get(pages[0]);
    if (!ref || pages.includes(ref)) break;
    pages.unshift(ref);
  }
  return pages.filter((u) => u && u !== downloadUrl && isHttp(u));
}

// The landing page: the page that started the download is usually generated
// for this IP only, so step back that many pages, staying on the same site.
// Starting too early is harmless (you click through more); too late gets stuck.
function landingPage(pages, back) {
  if (!pages.length) return "";
  let i = pages.length - 1;
  const site = siteOf(pages[i]);
  for (let n = back; n > 0 && i > 0 && siteOf(pages[i - 1]) === site; n--) i--;
  return pages[i];
}

// The Cookie header from a cookies.getAll() result, for a download on host:
// with first-party isolation on, only cookies of that site (or of no first
// party) count. SameSite is not filtered.
function cookieHeaderFor(cookies, host) {
  return cookies
    .filter((c) => !c.firstPartyDomain || host === c.firstPartyDomain || host.endsWith("." + c.firstPartyDomain))
    .map((c) => `${c.name}=${c.value}`)
    .join("; ");
}

// How many pages back from the one that started a download its landing page
// is: what recording learned for that host (stepBacks: host -> pages back),
// else one page.
function stepBackFor(stepBacks, pages) {
  const n = pages.length ? stepBacks[hostOf(pages[pages.length - 1])] : undefined;
  return Number.isInteger(n) ? n : 1;
}

// What a recording teaches: how many pages before the one that started the
// download the page of the first click was; null if it isn't among them.
function learnedStepBack(pages, firstClickUrl) {
  const i = pages.lastIndexOf(firstClickUrl);
  return i < 0 ? null : pages.length - 1 - i;
}

if (typeof module !== "undefined") {
  module.exports = {
    headerMap, mediaType, isPage, isAttachment, looksLikeDownload, isTorrentFile, responseSize,
    dispositionName, hostOf, siteOf, isHttp, captureResponse, captureDownload, pagesFor, landingPage,
    stepBackFor, learnedStepBack, cookieHeaderFor,
  };
}
