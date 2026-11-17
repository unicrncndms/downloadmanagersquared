"use strict";

// The browser.* seams the background scripts share. The pure rules they use
// are in lib/.

// Chromium MV3 without the webRequestBlocking permission cannot register a blocking listener (it throws):
// there downloads.onCreated does the whole capture. Firefox and MV2 always can.
const canBlock = () => {
  const m = browser.runtime.getManifest();
  return m.manifest_version !== 3 || (m.permissions || []).includes("webRequestBlocking");
};

// For a promise nobody waits on: its failure is logged, not an unhandled rejection.
const quiet = (p) => Promise.resolve(p).catch((e) => console.error(e));

// Cancels a browser download and removes it from the list.
async function discardDownload(id) {
  try {
    await browser.downloads.cancel(id);
    await browser.downloads.erase({ id });
  } catch (e) { /* already gone */ }
}

// The Cookie header the browser sent with each request still being looked
// at, by requestId: the last one across redirects. Filled from onSendHeaders;
// take() hands it over once the response has been handled. The same listener
// hands Capture the Referer, so a document load's request headers are read once.
const SentCookies = {
  byRequest: new Map(),

  init() {
    const filter = { urls: ["<all_urls>"], types: ["main_frame", "sub_frame"] };
    browser.webRequest.onSendHeaders.addListener((d) => {
      let cookie, referer;
      for (const x of d.requestHeaders || []) {
        const n = x.name.toLowerCase();
        if (n === "cookie") cookie = x.value;
        else if (n === "referer") referer = x.value;
      }
      if (cookie !== undefined && canBlock()) this.byRequest.set(d.requestId, cookie);
      else this.byRequest.delete(d.requestId);
      if (d.type === "main_frame") Capture.noteReferer(d, referer);
    }, filter, ["requestHeaders", ...(browser.webRequest.OnSendHeadersOptions?.EXTRA_HEADERS ? ["extraHeaders"] : [])]); // Chromium hides Referer and Cookie without extraHeaders
    browser.webRequest.onErrorOccurred.addListener((d) => this.byRequest.delete(d.requestId), filter);
  },

  // The header sent for a request whose response is handled now, or undefined.
  take(requestId) {
    const v = this.byRequest.get(requestId);
    this.byRequest.delete(requestId);
    return v;
  },
};

// copyCookies puts the cookies store `from` would send to url (the landing
// page: its host and parent domains, no other site) into store `to`, so a
// resolve's container starts logged in where the user is. First-party
// isolation keys and partitions are kept. Nothing is copied back. Returns
// how many were copied.
async function copyCookies(url, from, to) {
  if (from === "firefox-private") return 0; // a private window's login stays in it
  let cookies = [];
  try {
    cookies = await browser.cookies.getAll({ url, storeId: from, firstPartyDomain: null, partitionKey: {} });
  } catch (e) {
    try { cookies = await browser.cookies.getAll({ url, storeId: from }); } catch (e2) { return 0; }
  }
  // In parallel: one round trip, not one per cookie.
  const done = await Promise.all(cookies.map(async (c) => {
    const host = c.domain.replace(/^\./, "");
    const set = {
      url: `${c.secure ? "https" : "http"}://${host}${c.path || "/"}`,
      name: c.name, value: c.value, path: c.path, secure: c.secure, httpOnly: c.httpOnly, storeId: to,
    };
    if (!c.hostOnly) set.domain = c.domain;
    if (!c.session && c.expirationDate) set.expirationDate = c.expirationDate;
    if (c.sameSite) set.sameSite = c.sameSite;
    if (c.firstPartyDomain) set.firstPartyDomain = c.firstPartyDomain;
    if (c.partitionKey) set.partitionKey = c.partitionKey;
    try {
      await browser.cookies.set(set);
      return 1;
    } catch (e) { return 0; /* one the container refuses: the page may ask to log in */ }
  }));
  return done.reduce((a, b) => a + b, 0);
}

// Fallback where no request passed by (context menu, browser downloads): the
// cookies this container holds for the URL, first-party filtered.
async function cookieHeader(url, storeId) {
  let cookies = [];
  // Chromium rejects an undefined storeId key and firstPartyDomain; only a browser with containers has the latter.
  const q = { url, ...(storeId !== undefined && { storeId }) };
  try {
    cookies = await browser.cookies.getAll(browser.contextualIdentities ? { url, storeId, firstPartyDomain: null } : q);
  } catch (e) {
    try { cookies = await browser.cookies.getAll(q); } catch (e2) { /* none */ }
  }
  return cookieHeaderFor(cookies, hostOf(url));
}

function notify(title, message) {
  browser.notifications.create({
    type: "basic",
    iconUrl: browser.runtime.getURL("icons/icon.svg"),
    title,
    message,
  });
}
