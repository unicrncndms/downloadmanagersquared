"use strict";

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

// Something Firefox would save rather than render.
function looksLikeDownload(h) {
  if (isAttachment(h)) return true;
  const t = mediaType(h);
  return !/^(text\/|image\/|application\/(xhtml\+xml|xml|json|javascript|pdf))/.test(t);
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

// Rough "same site" key: the last two labels of the host name.
function siteOf(url) {
  return hostOf(url).split(".").slice(-2).join(".");
}

function hostOf(url) {
  try { return new URL(url).hostname; } catch (e) { return ""; }
}

// Cookie header the site would get from this container for this URL.
async function cookieHeader(url, storeId) {
  let cookies = [];
  try {
    cookies = await browser.cookies.getAll({ url, storeId, firstPartyDomain: null });
  } catch (e) {
    try { cookies = await browser.cookies.getAll({ url, storeId }); } catch (e2) { /* none */ }
  }
  return cookies.map((c) => `${c.name}=${c.value}`).join("; ");
}

function notify(title, message) {
  browser.notifications.create({
    type: "basic",
    iconUrl: browser.runtime.getURL("icons/icon.svg"),
    title,
    message,
  });
}
