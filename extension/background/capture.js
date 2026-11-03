"use strict";

// Takes over big downloads, magnet links and .torrent files from Firefox and
// hands them to the engine.
const Capture = {
  history: new Map(),  // tabId -> recent pages shown in that tab, oldest first
  referers: new Map(), // page URL -> the page it was opened from (its Referer)
  stepBacks: {},       // host -> how many pages before the download its landing page is, as recorded
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
    browser.storage.local.get(["stepBacks", "pagesById"]).then((s) => {
      this.stepBacks = s.stepBacks || {};
      this.pagesById = s.pagesById || {};
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
    const cookie = SentCookies.take(d.requestId); // dropped whatever happens to the response
    if (Resolver.ownsStore(d.cookieStoreId)) return Resolver.onHeaders(d, cookie);
    if (d.method !== "GET") return {};
    const h = headerMap(d.responseHeaders);
    if (!Engine.connected || !Engine.settings) return {};
    const kind = captureResponse(h, d.url, d.statusCode, Engine.settings.thresholdBytes);
    if (!kind) return {};
    if (this.bypass.delete(d.url)) return {};
    this.handOff({
      torrent: kind === "torrent",
      url: d.url,
      pages: this.pagesFor(d.tabId, d.url, [d.originUrl, d.documentUrl]),
      filename: dispositionName(h),
      cookieStoreId: d.cookieStoreId,
      cookie: cookie || "",
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
    const kind = captureDownload(item, Engine.settings.thresholdBytes);
    if (!kind) return;
    await discardDownload(item.id);
    this.handOff({
      torrent: kind === "torrent",
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

  // Pages leading up to the download in a tab (-1: none known), oldest first.
  pagesFor(tabId, downloadUrl, hints) {
    return pagesFor(tabId >= 0 ? this.history.get(tabId) : [], this.referers, hints, downloadUrl);
  },

  // Unknown hosts start one page back on the same site; recorded ones where
  // their first click was.
  landingPage(pages) {
    return landingPage(pages, stepBackFor(this.stepBacks, pages));
  },

  // A recording for download id started clicking on firstClickUrl: remember how
  // far back that was for the host the download started on.
  learnStepBack(id, firstClickUrl) {
    const pages = this.pagesById[id] || [];
    const n = learnedStepBack(pages, firstClickUrl);
    if (n === null) return;
    this.stepBacks[hostOf(pages[pages.length - 1])] = n;
    browser.storage.local.set({ stepBacks: this.stepBacks });
  },

  // cookie: the header the browser sent with the download's request; without
  // a request (context menu, browser downloads) the container's cookies.
  async handOff({ url, pageUrl, pages, filename, cookieStoreId, cookie, tabId, closeBlankTab, torrent }) {
    pages = pages || (pageUrl ? [pageUrl] : []);
    if (pageUrl === undefined) pageUrl = this.landingPage(pages);
    const referer = pages[pages.length - 1] || pageUrl;
    if (cookie === undefined) cookie = await cookieHeader(url, cookieStoreId);
    try {
      const { id } = await Engine.call("add", { url, pageUrl, filename, cookie, referer, userAgent: navigator.userAgent, torrent: !!torrent });
      if (torrent) {
        this.torrentAdded();
      } else {
        this.rememberPages(id, pages);
        notify("Downloading with DMS²", filename || "download started");
      }
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

  // A magnet link clicked in a page (content/magnet.js caught the click).
  async takeMagnet(url, tab) {
    try {
      await Engine.call("add", { url });
      this.torrentAdded();
    } catch (e) {
      notify("DMS² couldn't take this magnet link", `${e.message}. Firefox will handle it instead.`);
      if (tab) browser.tabs.update(tab.id, { url }).catch(() => {});
    }
  },

  // A torrent waits for its files to be chosen, so show the add step.
  torrentAdded() {
    notify("Torrent added to DMS²", "Choose its files in the DMS² downloads list.");
    openDownloads().catch(() => {});
  },

  rememberPages(id, pages) {
    this.pagesById[id] = pages;
    const ids = Object.keys(this.pagesById);
    for (const old of ids.slice(0, Math.max(0, ids.length - 50))) delete this.pagesById[old];
    browser.storage.local.set({ pagesById: this.pagesById });
  },
};
