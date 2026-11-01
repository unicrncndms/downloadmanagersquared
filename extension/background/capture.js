"use strict";

// Takes over big downloads from Firefox and hands them to the engine.
const Capture = {
  history: new Map(),  // tabId -> recent pages shown in that tab, oldest first
  referers: new Map(), // page URL -> the page it was opened from (its Referer)
  landingBack: 1,      // how many pages before the download the landing page is
  pagesById: {},       // download id -> page history, for the resolver's "previous page" button
  bypass: new Set(),   // URLs we handed back to Firefox after the engine refused them

  init() {
    browser.webRequest.onHeadersReceived.addListener(
      (d) => this.onHeaders(d),
      { urls: ["<all_urls>"], types: ["main_frame", "sub_frame"] },
      ["blocking", "responseHeaders"],
    );
    browser.downloads.onCreated.addListener((item) => this.onDownload(item));
    // Every page a tab shows, including ones served from cache, Back/Forward and
    // in-page navigation; network responses alone miss those.
    const onNav = (d) => {
      if (d.frameId === 0 && isHttp(d.url)) this.recordPage(d.tabId, d.url);
    };
    browser.webNavigation.onCommitted.addListener(onNav);
    browser.webNavigation.onHistoryStateUpdated.addListener(onNav);
    // Each page's referrer is the page it came from: that recovers the landing page
    // even if it was loaded before the extension started.
    browser.webRequest.onBeforeSendHeaders.addListener((d) => {
      const ref = (d.requestHeaders || []).find((x) => x.name.toLowerCase() === "referer");
      if (!ref || !isHttp(ref.value)) return;
      this.referers.set(d.url, ref.value);
      if (this.referers.size > 300) this.referers.delete(this.referers.keys().next().value);
    }, { urls: ["<all_urls>"], types: ["main_frame"] }, ["requestHeaders"]);
    // Pages already open when the extension starts.
    browser.tabs.query({}).then((tabs) => {
      for (const t of tabs) if (isHttp(t.url) && !this.history.has(t.id)) this.recordPage(t.id, t.url);
    });
    browser.tabs.onRemoved.addListener((id) => this.history.delete(id));
    // A tab opened from another one (downloads often open in a new tab) inherits its history.
    browser.tabs.onCreated.addListener((tab) => {
      const h = tab.openerTabId !== undefined && this.history.get(tab.openerTabId);
      if (h) this.history.set(tab.id, h.slice());
    });
    browser.storage.local.get(["landingBack", "pagesById"]).then((s) => {
      if (Number.isInteger(s.landingBack)) this.landingBack = s.landingBack;
      this.pagesById = s.pagesById || {};
    });
    browser.storage.onChanged.addListener((c) => {
      if (c.landingBack && Number.isInteger(c.landingBack.newValue)) this.landingBack = c.landingBack.newValue;
    });

    browser.contextMenus.create({ id: "dms-link", title: "Download with DMS²", contexts: ["link"] });
    browser.contextMenus.create({ id: "dms-page", title: "Download this page's file with DMS²", contexts: ["page"] });
    browser.contextMenus.onClicked.addListener((info, tab) => {
      if (info.menuItemId === "dms-link") {
        this.handOff({ url: info.linkUrl, pageUrl: tab.url, cookieStoreId: tab.cookieStoreId });
      } else if (info.menuItemId === "dms-page") {
        this.handOff({ url: tab.url, pageUrl: tab.url, cookieStoreId: tab.cookieStoreId });
      }
    });
  },

  onHeaders(d) {
    if (Resolver.ownsStore(d.cookieStoreId)) return Resolver.onHeaders(d);
    if (d.method !== "GET") return {};
    const h = headerMap(d.responseHeaders);
    if (isPage(h)) return {};
    if (!Engine.connected || !Engine.settings) return {};
    if (d.statusCode !== 200 && d.statusCode !== 206) return {};
    if (this.bypass.delete(d.url)) return {};
    if (responseSize(h, d.statusCode) < Engine.settings.thresholdBytes || !looksLikeDownload(h)) return {};
    this.handOff({
      url: d.url,
      pages: this.pagesFor(d.tabId, d.url, [d.originUrl, d.documentUrl]),
      filename: dispositionName(h),
      cookieStoreId: d.cookieStoreId,
      tabId: d.tabId,
      closeBlankTab: d.type === "main_frame",
    });
    return { cancel: true };
  },

  // Fallback for downloads that didn't pass through onHeaders (e.g. <a download>).
  async onDownload(item) {
    if (Resolver.ownsStore(item.cookieStoreId)) return Resolver.onDownload(item);
    if (!Engine.connected || !Engine.settings || item.byExtensionId === browser.runtime.id) return;
    if (this.bypass.delete(item.url)) return;
    const size = item.totalBytes > 0 ? item.totalBytes : item.fileSize;
    if (!(size >= Engine.settings.thresholdBytes)) return;
    await discardDownload(item.id);
    this.handOff({
      url: item.finalUrl || item.url,
      pages: this.pagesFor(-1, item.finalUrl || item.url, [item.referrer]),
      filename: (item.filename || "").split(/[\\/]/).pop(),
      cookieStoreId: item.cookieStoreId,
    });
  },

  recordPage(tabId, url) {
    const h = this.history.get(tabId) || [];
    if (h[h.length - 1] !== url) h.push(url);
    if (h.length > 15) h.shift();
    this.history.set(tabId, h);
  },

  // Pages leading up to the download, oldest first, ending with the page that
  // started it. Never includes the download link itself.
  pagesFor(tabId, downloadUrl, hints) {
    const pages = ((tabId >= 0 && this.history.get(tabId)) || []).slice();
    for (const u of hints) {
      if (u && !pages.includes(u)) pages.push(u);
    }
    // Extend backwards through referrers for pages from before we were watching.
    for (let i = 0; i < 5 && pages.length; i++) {
      const ref = this.referers.get(pages[0]);
      if (!ref || pages.includes(ref)) break;
      pages.unshift(ref);
    }
    return pages.filter((u) => u && u !== downloadUrl && isHttp(u));
  },

  // The landing page: the page that started the download is usually generated for
  // this IP only, so step back (landingBack pages), staying on the same site.
  // Starting too early is harmless (you click through more); too late gets stuck.
  landingPage(pages) {
    if (!pages.length) return "";
    let i = pages.length - 1;
    const site = siteOf(pages[i]);
    for (let n = this.landingBack; n > 0 && i > 0 && siteOf(pages[i - 1]) === site; n--) i--;
    return pages[i];
  },

  async handOff({ url, pageUrl, pages, filename, cookieStoreId, tabId, closeBlankTab }) {
    pages = pages || (pageUrl ? [pageUrl] : []);
    if (pageUrl === undefined) pageUrl = this.landingPage(pages);
    const referer = pages[pages.length - 1] || pageUrl;
    const cookie = await cookieHeader(url, cookieStoreId);
    try {
      const { id } = await Engine.call("add", { url, pageUrl, filename, cookie, referer, userAgent: navigator.userAgent });
      this.rememberPages(id, pages);
      notify("Downloading with DMS²", filename || "download started");
    } catch (e) {
      // Don't lose the download: give it back to Firefox.
      notify("DMS² couldn't take this download", `${e.message}. Firefox will download it instead.`);
      this.bypass.add(url);
      browser.downloads.download({ url, filename: filename || undefined, cookieStoreId }).catch(() => {});
    }
    if (closeBlankTab && tabId >= 0) {
      const tab = await browser.tabs.get(tabId).catch(() => null);
      if (tab && (tab.url === "about:blank" || tab.url === url)) browser.tabs.remove(tabId).catch(() => {});
    }
  },

  rememberPages(id, pages) {
    this.pagesById[id] = pages;
    const ids = Object.keys(this.pagesById);
    for (const old of ids.slice(0, Math.max(0, ids.length - 50))) delete this.pagesById[old];
    browser.storage.local.set({ pagesById: this.pagesById });
  },
};
