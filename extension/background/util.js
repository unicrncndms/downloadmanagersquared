"use strict";

// The browser.* seams the background scripts share. The pure rules they use
// are in lib/.

// Cancels a browser download and removes it from the list.
async function discardDownload(id) {
  try {
    await browser.downloads.cancel(id);
    await browser.downloads.erase({ id });
  } catch (e) { /* already gone */ }
}

// The Cookie header the browser sent with each request still being looked
// at, by requestId: the last one across redirects. Filled from onSendHeaders;
// take() hands it over once the response has been handled.
const SentCookies = {
  byRequest: new Map(),

  init() {
    const filter = { urls: ["<all_urls>"], types: ["main_frame", "sub_frame"] };
    browser.webRequest.onSendHeaders.addListener((d) => {
      const h = (d.requestHeaders || []).find((x) => x.name.toLowerCase() === "cookie");
      if (h) this.byRequest.set(d.requestId, h.value);
      else this.byRequest.delete(d.requestId);
    }, filter, ["requestHeaders"]);
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
  let n = 0;
  for (const c of cookies) {
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
      n++;
    } catch (e) { /* one the container refuses: the page may ask to log in */ }
  }
  return n;
}

// Fallback where no request passed by (context menu, browser downloads): the
// cookies this container holds for the URL, first-party filtered.
async function cookieHeader(url, storeId) {
  let cookies = [];
  try {
    cookies = await browser.cookies.getAll({ url, storeId, firstPartyDomain: null });
  } catch (e) {
    try { cookies = await browser.cookies.getAll({ url, storeId }); } catch (e2) { /* none */ }
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
