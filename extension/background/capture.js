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
    if (canBlock()) {
      browser.webRequest.onHeadersReceived.addListener(
        (d) => this.onHeaders(d),
        { urls: ["<all_urls>"], types: ["main_frame", "sub_frame"] },
        ["blocking", "responseHeaders"],
      );
    }
    browser.downloads.onCreated.addListener((item) => this.onDownload(item));
    // Every page a tab shows, including ones served from cache, Back/Forward and
    // in-page navigation; network responses alone miss those.
    const onNav = (d) => {
      if (d.frameId === 0 && isHttp(d.url)) this.recordPage(d.tabId, d.url);
    };
    browser.webNavigation.onCommitted.addListener(onNav);
    browser.webNavigation.onHistoryStateUpdated.addListener(onNav);
    // Pages already open when the extension starts.
    browser.tabs.query({}).then((tabs) => {
      for (const t of tabs) if (isHttp(t.url) && !this.history.has(t.id)) this.recordPage(t.id, t.url);
    });
    browser.tabs.onRemoved.addListener((id) => { this.history.delete(id); this.saveHistory(); });
    // Chromium's background sleeps after 30 s idle and wakes with no history: it
    // outlives that in storage.session. What was seen since waking goes after it.
    this.historyRead = Promise.resolve(browser.storage.session && browser.storage.session.get("tabHistory")).then((s) => {
      for (const [id, old] of Object.entries((s && s.tabHistory) || {})) {
        const h = old.slice();
        for (const u of this.history.get(+id) || []) if (h[h.length - 1] !== u) h.push(u);
        this.history.set(+id, h.slice(-15));
      }
    }).catch(() => {});
    // A tab opened from another one (downloads often open in a new tab) inherits its history.
    browser.tabs.onCreated.addListener((tab) => {
      const h = tab.openerTabId !== undefined && this.history.get(tab.openerTabId);
      if (h) { this.history.set(tab.id, h.slice()); this.saveHistory(); }
    });
    // The engine resolves this browser's downloads in a browser of its own
    // (Chromium): it asks for the landing page's login and the record bar's
    // way back, and says where a recorded first click was.
    // A failure is said, so the engine logs why and goes on at once with the download's cookies.
    Engine.on("pageInfo", (r) => this.pageInfo(r).catch((e) => Engine.call("pageInfo", { id: r.id, error: String((e && e.message) || e) }).catch(() => {})));
    Engine.on("stepBack", ({ id, url }) => this.stored.then(() => this.learnStepBack(id, url)));
    // handOff waits for it: a read finishing late would replace the pages it just recorded.
    this.stored = browser.storage.local.get(["stepBacks", "pagesById"]).then((s) => {
      this.stepBacks = s.stepBacks || {};
      this.pagesById = s.pagesById || {};
    }).catch(() => {});
    // Step-backs are shared through the engine (ticket 36): this browser's own go to it
    // on connect (it keeps those it knows), and every change comes back as an event.
    Engine.on("stepBacks", (m) => this.stored.then(() => this.setStepBacks(m)));
    Engine.on("connection", ({ connected }) => {
      if (connected) this.stored.then(() => Engine.call("stepBacks", { stepBacks: this.stepBacks })).then((m) => this.setStepBacks(m), () => {});
    });

    // Firefox menus do not persist: made at every start. Chromium's do, and creating an id twice fails: made once, on install.
    if (browser.runtime.getManifest().manifest_version === 3) browser.runtime.onInstalled.addListener(() => this.createMenus());
    else this.createMenus();
    browser.contextMenus.onClicked.addListener((info, tab) => {
      if (info.menuItemId === "dms-link") {
        this.handOff({ url: info.linkUrl, pageUrl: tab.url, cookieStoreId: tab.cookieStoreId });
      } else if (info.menuItemId === "dms-page") {
        this.handOff({ url: tab.url, pageUrl: tab.url, cookieStoreId: tab.cookieStoreId });
      }
    });
  },

  createMenus() {
    browser.contextMenus.create({ id: "dms-link", title: "Download with DMS²", contexts: ["link"] });
    // The page item means something only on a file's own address or a learned site: Firefox asks
    // again each time the menu opens (menus.onShown); without that hook it always shows.
    const onShown = browser.menus && browser.menus.onShown;
    browser.contextMenus.create({ id: "dms-page", title: "Download this page's file with DMS²", contexts: ["page"], visible: !onShown });
    if (onShown) {
      onShown.addListener(async (info, tab) => {
        const show = !!info.menuIds.includes("dms-page") && this.pageMenuFits(tab && tab.url);
        if (this.pageMenuShown === show) return;
        this.pageMenuShown = show;
        await browser.menus.update("dms-page", { visible: show });
        browser.menus.refresh();
      });
    }
  },

  pageMenuShown: false,

  // Each page's referrer is the page it came from: that recovers the landing page
  // even if it was loaded before the extension started. Called by SentCookies.
  noteReferer(d, referer) {
    if (!referer || !isHttp(referer)) return;
    this.referers.set(d.url, referer);
    if (this.referers.size > 300) this.referers.delete(this.referers.keys().next().value);
  },

  // Whether "Download this page's file" can mean something on this page.
  pageMenuFits(url) {
    return pageMenuFits(url, Resolver.recipes);
  },

  onHeaders(d, cookie = SentCookies.take(d.requestId)) { // dropped whatever happens to the response
    if (!Resolver.loaded) return Resolver.ready.then(() => this.onHeaders(d, cookie));
    const owner = Resolver.ownerOf(d);
    if (owner) return Resolver.onHeaders(d, cookie, owner);
    if (d.method !== "GET" || (d.statusCode !== 200 && d.statusCode !== 206)) return {}; // captureResponse takes nothing else
    if (!Engine.connected || !Engine.settings) return {};
    // An ordinary page is nearly every response: leave it before the whole map is built.
    const type = (d.responseHeaders || []).find((x) => x.name.toLowerCase() === "content-type");
    if (type && isPage({ "content-type": type.value })) return {};
    const h = headerMap(d.responseHeaders);
    const kind = captureResponse(h, d.url, d.statusCode, thresholdFor(Engine.settings, d.url, d.originUrl, d.documentUrl));
    if (!kind || neverCaptured(Engine.settings.neverCapture, d.url, d.originUrl, d.documentUrl)) return {};
    if (this.bypass.delete(d.url)) return {};
    this.handOff({
      torrent: kind === "torrent",
      metalink: kind === "metalink",
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

  // Downloads that didn't pass through onHeaders (e.g. <a download>), and,
  // where there is no blocking headers hook (Chromium), every download: the
  // same thresholds and the same hand-off, after the browser started it.
  async onDownload(item) {
    await Promise.all([Resolver.ready, this.historyRead]);
    const owner = Resolver.ownerOf(item);
    if (owner) return Resolver.onDownload(item, owner);
    if (!Engine.connected || !Engine.settings || item.byExtensionId === browser.runtime.id) return;
    const url = item.finalUrl || item.url;
    if (this.bypass.delete(item.url) || this.bypass.delete(url)) return;
    const kind = captureDownload(item, thresholdFor(Engine.settings, url, item.referrer));
    if (!kind || neverCaptured(Engine.settings.neverCapture, url, item.referrer)) return;
    if (!isHttp(url)) return kind === "download" ? undefined : this.takeSaved(item, kind === "torrent");
    await discardDownload(item.id);
    this.handOff({
      torrent: kind === "torrent",
      metalink: kind === "metalink",
      url,
      pages: this.downloadPages(item.referrer, url),
      filename: (item.filename || "").split(/[\\/]/).pop(),
      cookieStoreId: item.cookieStoreId,
    });
  },

  // A .torrent or Metalink a page made itself (a blob: or data: link): only the
  // browser can fetch it, so it saves the file and the engine reads that.
  async takeSaved(item, torrent) {
    const state = async () => ((await browser.downloads.search({ id: item.id }))[0] || { state: "interrupted" });
    await new Promise((resolve) => {
      const check = async () => {
        if ((await state()).state === "in_progress") return;
        browser.downloads.onChanged.removeListener(check);
        resolve();
      };
      browser.downloads.onChanged.addListener(check);
      check();
    });
    const saved = await state();
    if (saved.state !== "complete") return;
    try {
      await Engine.call("add", { filePath: saved.filename, userAgent: navigator.userAgent, cookieStore: item.cookieStoreId || "" });
      if (torrent) this.torrentAdded();
    } catch (e) {
      notify("DMS² couldn't take this file", `${e.message}. Firefox saved it as ${saved.filename}.`);
    }
  },

  // The tab whose current page is url (a download's referrer), -1 if none:
  // a download item names no tab, so this finds the history to use.
  tabShowing(url) {
    if (!url) return -1;
    for (const [tabId, h] of this.history) if (h[h.length - 1] === url) return tabId;
    return -1;
  },

  // The pages before a download the browser started. Chrome names only the site
  // (https://host/) as the referrer of a download from another site: then the
  // tab that was last on that site is the one, and the bare address isn't a page
  // of it (taken as one, the resolve started on a stale page of an earlier download).
  downloadPages(referrer, url) {
    let tab = this.tabShowing(referrer);
    if (tab < 0 && /^https?:\/\/[^/]+\/$/.test(referrer || "")) {
      for (const [id, h] of this.history) if (h.length && h[h.length - 1].startsWith(referrer)) tab = id; // the Map is in order of use: the last is the latest
      if (tab >= 0) return this.pagesFor(tab, url, []);
    }
    return this.pagesFor(tab, url, [referrer]);
  },

  recordPage(tabId, url) {
    const h = this.history.get(tabId) || [];
    if (h[h.length - 1] !== url) h.push(url);
    if (h.length > 15) h.shift();
    this.history.delete(tabId); // to the end: tabs in order of use
    this.history.set(tabId, h);
    this.saveHistory();
  },

  // After the stored histories are read back: written earlier, it would replace them.
  saveHistory() {
    if (!browser.storage.session) return;
    this.historyRead.then(() => browser.storage.session.set({ tabHistory: Object.fromEntries(this.history) })).catch(() => {});
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
    const host = hostOf(pages[pages.length - 1]);
    this.stepBacks[host] = n;
    browser.storage.local.set({ stepBacks: this.stepBacks });
    Engine.call("stepBacks", { stepBacks: { [host]: n }, set: true }).catch(() => {});
  },

  // The engine's step-backs; an engine older than them answers nothing to keep.
  setStepBacks(m) {
    if (!m || typeof m !== "object") return;
    this.stepBacks = m;
    browser.storage.local.set({ stepBacks: m });
  },

  // pageInfo answers the engine: this browser's cookies for the landing page
  // (none from a private window: its login stays in it), the download's page
  // history and the accent.
  async pageInfo({ id, pageUrl, cookieStore }) {
    await this.stored;
    let cookies = [];
    if (cookieStore !== "1" && cookieStore !== "firefox-private") {
      const q = { url: pageUrl, ...(cookieStore && { storeId: cookieStore }) };
      try {
        cookies = await browser.cookies.getAll({ ...q, partitionKey: {} });
      } catch (e) {
        try { cookies = await browser.cookies.getAll(q); } catch (e2) { /* none */ }
      }
    }
    const tokens = accentTokens(pageAccent(Engine.settings)) || accentTokens("#1d4ed8");
    await Engine.call("pageInfo", { id, cookies: cookies.map(cdpCookie), pages: this.pagesById[id] || [], tokens });
  },

  // cookie: the header the browser sent with the download's request; without
  // a request (context menu, browser downloads) the container's cookies.
  async handOff({ url, pageUrl, pages, filename, cookieStoreId, cookie, tabId, closeBlankTab, torrent, metalink }) {
    await this.stored;
    pages = pages || (pageUrl ? [pageUrl] : []);
    if (pageUrl === undefined) pageUrl = this.landingPage(pages);
    const referer = pages[pages.length - 1] || pageUrl;
    if (cookie === undefined) cookie = await cookieHeader(url, cookieStoreId);
    try {
      const r = await Engine.call("add", { url, pageUrl, filename, cookie, referer, userAgent: navigator.userAgent, torrent: !!torrent, metalink: !!metalink, cookieStore: cookieStoreId || "", ask: true });
      if (r.skipped) {
        notify("DMS² didn't add this download", r.skipped);
      } else if (r.ask) {
        // Held by the engine until the user answers above the downloads list.
        notify("DMS² has a question", "A file with this name is already there. Answer in the DMS² downloads list.");
        Engine.call("openManager", {}).catch(() => {});
      } else if (torrent) {
        this.torrentAdded();
      } else {
        this.rememberPages(r.id, pages); // no notice: the toolbar button shows it running, and the engine speaks when it ends
      }
    } catch (e) {
      // Don't lose the download: give it back to Firefox.
      this.bypass.add(url);
      // A server-chosen filename Firefox rejects must not lose the download: retry without it.
      const store = cookieStoreId ? { cookieStoreId } : {}; // Chromium rejects the key where there are no containers
      browser.downloads.download({ url, filename: filename || undefined, ...store })
        .catch((err) => { if (!filename) throw err; return browser.downloads.download({ url, ...store }); })
        .then(() => notify("DMS² couldn't take this download", `${e.message}. Firefox will download it instead.`),
          () => { this.bypass.delete(url); notify("DMS² couldn't take this download", `${e.message}. Firefox couldn't download it either.`); });
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

  // An ftp:// or ftps:// link clicked in a page: Firefox can't open it, the engine can.
  async takeFtp(url) {
    if (!/^ftps?:/i.test(url)) return;
    try {
      await Engine.call("add", { url });
      notify("Added to DMS²", "The FTP download is in the DMS² downloads list.");
    } catch (e) {
      notify("DMS² couldn't take this FTP link", e.message);
    }
  },

  // A torrent waits for its files to be chosen, so show the add step.
  torrentAdded() {
    notify("Torrent added to DMS²", "Choose its files in the DMS² downloads list.");
    Engine.call("openManager", {}).catch(() => {}); // the engine's Downloads window
  },

  rememberPages(id, pages) {
    this.pagesById[id] = pages;
    const ids = Object.keys(this.pagesById);
    for (const old of ids.slice(0, Math.max(0, ids.length - 50))) delete this.pagesById[old];
    browser.storage.local.set({ pagesById: this.pagesById });
  },
};
