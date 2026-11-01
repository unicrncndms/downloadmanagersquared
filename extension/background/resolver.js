"use strict";

// Gets a fresh download link for one interface: the landing page is opened in
// a container whose traffic goes through the engine's SOCKS proxy for that
// interface, the remembered clicks are replayed, and the download the site
// starts is caught and handed to the engine instead.
const Resolver = {
  containers: {},   // ifaceId -> cookieStoreId
  ifaceByStore: {}, // cookieStoreId -> ifaceId
  recipes: {},      // landing-page host -> { steps, learned }
  jobs: new Map(),  // "downloadId|ifaceId" -> job
  tabJobs: new Map(),
  creating: new Map(),

  REPLAY_TIMEOUT: 120000,
  RECORD_TIMEOUT: 15 * 60000,
  DOWNLOAD_WAIT: 60000,
  COLORS: ["blue", "turquoise", "green", "yellow", "orange", "red", "pink", "purple"],

  async init() {
    const s = await browser.storage.local.get(["containers", "recipes"]);
    this.containers = s.containers || {};
    this.recipes = s.recipes || {};
    for (const [iface, store] of Object.entries(this.containers)) {
      try { await browser.contextualIdentities.get(store); } catch (e) { delete this.containers[iface]; }
    }
    this.reindex();

    browser.proxy.onRequest.addListener((d) => this.proxyFor(d), { urls: ["<all_urls>"] });
    browser.proxy.onError.addListener((e) => console.warn("DMS² proxy:", e.message));
    browser.contextualIdentities.onRemoved.addListener(({ contextualIdentity }) => {
      for (const [iface, store] of Object.entries(this.containers)) {
        if (store === contextualIdentity.cookieStoreId) delete this.containers[iface];
      }
      this.saveContainers();
    });
    browser.tabs.onUpdated.addListener((id, info, tab) => this.onTabUpdated(id, info, tab));
    browser.tabs.onCreated.addListener((tab) => this.onTabCreated(tab));
    browser.windows.onRemoved.addListener((wid) => this.onWindowRemoved(wid));
    browser.runtime.onMessage.addListener((msg, sender) => this.onContentMessage(msg, sender));
    Engine.on("resolve", (r) => this.request(r));
  },

  reindex() {
    this.ifaceByStore = {};
    for (const [iface, store] of Object.entries(this.containers)) this.ifaceByStore[store] = iface;
  },

  saveContainers() {
    this.reindex();
    browser.storage.local.set({ containers: this.containers });
  },

  async ensureContainer(iface, name) {
    if (this.containers[iface]) return this.containers[iface];
    if (!this.creating.has(iface)) {
      const color = this.COLORS[Object.keys(this.containers).length % this.COLORS.length];
      this.creating.set(iface, browser.contextualIdentities
        .create({ name: `DMS² · ${name || iface}`, color, icon: "circle" })
        .then((c) => {
          this.containers[iface] = c.cookieStoreId;
          this.saveContainers();
          return c.cookieStoreId;
        })
        .finally(() => this.creating.delete(iface)));
    }
    return this.creating.get(iface);
  },

  proxyFor(d) {
    const iface = this.ifaceByStore[d.cookieStoreId];
    if (!iface) return { type: "direct" };
    const v = ((Engine.state && Engine.state.interfaces) || []).find((i) => i.id === iface);
    // Fail closed: a container page must never leave through the wrong connection.
    const port = Engine.connected && v && v.present ? v.socksPort : 1;
    return { type: "socks", host: "127.0.0.1", port, proxyDNS: true };
  },

  ownsStore(cookieStoreId) {
    return !!this.ifaceByStore[cookieStoreId];
  },

  request(r) {
    const key = `${r.id}|${r.iface}`;
    if (this.jobs.has(key)) return;
    const job = {
      key, id: r.id, iface: r.iface, ifaceName: r.ifaceName || r.iface, pageUrl: r.pageUrl,
      host: hostOf(r.pageUrl), mode: "new", step: 0, recordFrom: 0, recorded: [],
      tabs: new Set(), tabId: null, windowId: null, loading: false, skipped: false, timers: {},
    };
    this.jobs.set(key, job);
    this.startOrQueue(job);
  },

  startOrQueue(job) {
    if (this.recipes[job.host]) return this.launch(job, "replay");
    // First visit to this site: one job records the clicks, the others wait and replay them.
    const recorder = [...this.jobs.values()].find((j) => j !== job && j.host === job.host && j.mode === "record");
    if (recorder) {
      job.mode = "waiting";
      return;
    }
    return this.launch(job, "record");
  },

  async launch(job, mode) {
    let store;
    try {
      store = await this.ensureContainer(job.iface, job.ifaceName);
    } catch (e) {
      return this.fail(job, "could not create a container: " + e.message);
    }
    job.mode = mode;
    job.step = 0;
    job.recordFrom = 0;
    job.recorded = [];
    const visible = mode === "record";
    let win;
    try {
      win = await browser.windows.create({ url: job.pageUrl, cookieStoreId: store, state: visible ? "normal" : "minimized" });
    } catch (e) {
      return this.fail(job, "could not open the page: " + e.message);
    }
    job.windowId = win.id;
    job.tabId = win.tabs[0].id;
    job.tabs.add(job.tabId);
    this.tabJobs.set(job.tabId, job);
    if (visible) {
      notify("Show DMS² how to get the link",
        `Click through ${job.host} for ${job.ifaceName} as you normally would. Your clicks are remembered for next time.`);
    }
    this.setTimer(job, "overall", visible ? this.RECORD_TIMEOUT : this.REPLAY_TIMEOUT,
      () => this.fail(job, visible ? "nobody clicked through the page in time" : "timed out"));
  },

  setTimer(job, name, ms, fn) {
    clearTimeout(job.timers[name]);
    job.timers[name] = setTimeout(fn, ms);
  },

  clearTimers(job) {
    for (const t of Object.values(job.timers)) clearTimeout(t);
    job.timers = {};
  },

  steps(job) {
    return (this.recipes[job.host] && this.recipes[job.host].steps) || [];
  },

  onTabCreated(tab) {
    const job = tab.openerTabId !== undefined && this.tabJobs.get(tab.openerTabId);
    if (job) {
      job.tabs.add(tab.id);
      this.tabJobs.set(tab.id, job);
    }
  },

  onTabUpdated(tabId, info, tab) {
    const job = this.tabJobs.get(tabId);
    if (!job || job.done || tabId !== job.tabId) return;
    if (info.status === "loading") job.loading = true;
    if (info.status === "complete") {
      job.loading = false;
      job.lastUrl = tab.url;
      // A new page means the captcha (if any) is behind us; the new page's script
      // reports straight away if there is another one.
      if (job.captcha) this.captchaDone(job);
      this.drive(job);
    }
  },

  async drive(job) {
    try {
      await browser.tabs.executeScript(job.tabId, { file: "/resolver-content.js", runAt: "document_idle" });
    } catch (e) {
      return; // e.g. an error page; the overall timer will catch a stuck job
    }
    if (job.mode === "record") {
      this.sendRecord(job);
    } else if (job.mode === "replay") {
      this.sendStep(job);
    }
  },

  // Recording, with a way back to the page before this one in case DMS² opened
  // a page that only worked for the first connection.
  async sendRecord(job) {
    const pages = Capture.pagesById[job.id] || [];
    const tab = await browser.tabs.get(job.tabId).catch(() => null);
    let i = tab ? pages.indexOf(tab.url) : -1;
    if (i < 0) i = pages.indexOf(job.pageUrl);
    const prev = i > 0 ? pages[i - 1] : null;
    browser.tabs.sendMessage(job.tabId, { cmd: "record", ifaceName: job.ifaceName, prev }).catch(() => {});
  },

  // A captcha needs a person: show the window and hold all timeouts until it's solved.
  captchaShown(job) {
    if (job.captcha) return;
    job.captcha = true;
    clearTimeout(job.timers.wait);
    this.setTimer(job, "overall", this.RECORD_TIMEOUT, () => this.fail(job, "the captcha wasn't solved in time"));
    browser.windows.update(job.windowId, { state: "normal", focused: true, drawAttention: true }).catch(() => {});
    notify("DMS² needs you to solve a captcha", `${job.host} asks for a captcha on ${job.ifaceName}. The window is open; DMS² continues once it's solved.`);
  },

  captchaDone(job) {
    job.captcha = false;
    if (job.mode !== "replay") {
      this.setTimer(job, "overall", this.RECORD_TIMEOUT, () => this.fail(job, "nobody clicked through the page in time"));
      return;
    }
    // Back out of the way; the page's script carries on with the next click by itself.
    browser.windows.update(job.windowId, { state: "minimized" }).catch(() => {});
    this.setTimer(job, "overall", this.REPLAY_TIMEOUT, () => this.fail(job, "timed out"));
    const steps = this.steps(job);
    if (job.step >= steps.length) {
      this.setTimer(job, "wait", this.DOWNLOAD_WAIT,
        () => this.switchToRecord(job, steps.length, "The download didn't start"));
    }
  },

  sendStep(job) {
    const steps = this.steps(job);
    if (job.step >= steps.length) {
      // All clicks done: the site's timer runs, then the download should start.
      if (job.captcha) return; // armed once the captcha is solved
      this.setTimer(job, "wait", this.DOWNLOAD_WAIT,
        () => this.switchToRecord(job, steps.length, "The download didn't start"));
      return;
    }
    browser.tabs.sendMessage(job.tabId, { cmd: "replay", step: steps[job.step], index: job.step }).catch(() => {});
  },

  switchToRecord(job, fromIndex, why) {
    if (job.done) return;
    job.mode = "record";
    job.recordFrom = fromIndex;
    job.recorded = [];
    this.clearTimers(job);
    browser.windows.update(job.windowId, { state: "normal", focused: true }).catch(() => {});
    notify("DMS² needs a hand", `${why} on ${job.host} (${job.ifaceName}). Please click through; your clicks will be remembered.`);
    this.sendRecord(job);
    this.setTimer(job, "overall", this.RECORD_TIMEOUT, () => this.fail(job, "nobody clicked through the page in time"));
  },

  onContentMessage(msg, sender) {
    if (!sender.tab || !msg || !msg.resolver) return;
    const job = this.tabJobs.get(sender.tab.id);
    if (!job || job.done) return;
    const steps = this.steps(job);
    switch (msg.type) {
      case "clicked":
        if (job.mode !== "replay") return;
        job.step = msg.index + 1;
        // If the click navigates, the next step is sent once the new page has loaded.
        setTimeout(() => {
          if (!job.done && !job.loading && job.mode === "replay") this.sendStep(job);
        }, 1500);
        break;
      case "notFound":
        if (job.mode !== "replay") return;
        // A remembered click may have been optional (e.g. a banner): try skipping it once.
        if (!job.skipped && msg.index + 1 < steps.length) {
          job.skipped = true;
          job.step = msg.index + 1;
          this.sendStep(job);
        } else {
          this.switchToRecord(job, msg.index, "Couldn't find the button to click");
        }
        break;
      case "captcha":
        this.captchaShown(job);
        break;
      case "captchaDone":
        if (job.captcha) this.captchaDone(job);
        break;
      case "recorded":
        if (job.mode !== "record") return;
        // The page of your first click is the real landing page: you may have had
        // to navigate there yourself if the page DMS² opened was the wrong one.
        if (job.recordFrom === 0 && job.recorded.length === 0) job.startUrl = sender.tab.url;
        job.recorded.push(msg.step);
        break;
    }
  },

  jobForStore(cookieStoreId, tabId) {
    const byTab = this.tabJobs.get(tabId);
    if (byTab && !byTab.done) return byTab;
    const iface = this.ifaceByStore[cookieStoreId];
    let found = null;
    for (const j of this.jobs.values()) {
      if (j.iface === iface && !j.done && (j.mode === "replay" || j.mode === "record")) found = j;
    }
    return found;
  },

  // Called by Capture for responses inside one of our containers.
  onHeaders(d) {
    const h = headerMap(d.responseHeaders);
    if (d.statusCode >= 300 || isPage(h) || !looksLikeDownload(h)) return {};
    const size = responseSize(h, d.statusCode);
    if (!isAttachment(h) && size >= 0 && size < (1 << 20)) return {};
    const job = this.jobForStore(d.cookieStoreId, d.tabId);
    if (!job) return {};
    this.finish(job, d.url);
    return { cancel: true };
  },

  async onDownload(item) {
    const job = this.jobForStore(item.cookieStoreId, -1);
    try {
      await browser.downloads.cancel(item.id);
      await browser.downloads.erase({ id: item.id });
    } catch (e) { /* already gone */ }
    if (job) this.finish(job, item.finalUrl || item.url);
  },

  async finish(job, url) {
    if (job.done) return;
    job.done = true;
    this.clearTimers(job);
    if (job.startUrl && job.startUrl !== job.pageUrl && /^https?:/.test(job.startUrl)) this.correctPage(job);
    if (job.mode === "record") this.saveRecipe(job);
    const cookie = await cookieHeader(url, this.containers[job.iface]);
    try {
      await Engine.call("resolved", { id: job.id, iface: job.iface, url, cookie, referer: job.lastUrl || job.pageUrl });
    } catch (e) {
      console.warn("DMS² could not report link:", e);
    }
    this.cleanup(job);
  },

  fail(job, error) {
    if (job.done) return;
    job.done = true;
    this.clearTimers(job);
    Engine.call("resolveFailed", { id: job.id, iface: job.iface, error }).catch(() => {});
    this.cleanup(job);
  },

  // Remember the landing page the user actually started from, for this download
  // and for the other connections still waiting to be resolved.
  correctPage(job) {
    const page = job.startUrl;
    job.pageUrl = page;
    job.host = hostOf(page);
    Engine.call("setPageUrl", { id: job.id, pageUrl: page }).catch(() => {});
    for (const j of this.jobs.values()) {
      if (j !== job && j.id === job.id && j.mode === "waiting") {
        j.pageUrl = page;
        j.host = job.host;
      }
    }
  },

  saveRecipe(job) {
    const old = this.steps(job);
    const steps = old.slice(0, job.recordFrom).concat(job.recorded);
    this.recipes[job.host] = { steps, learned: Date.now() };
    browser.storage.local.set({ recipes: this.recipes });
  },

  forgetRecipe(host) {
    delete this.recipes[host];
    return browser.storage.local.set({ recipes: this.recipes });
  },

  cleanup(job) {
    for (const t of job.tabs) this.tabJobs.delete(t);
    this.jobs.delete(job.key);
    if (job.windowId !== null) browser.windows.remove(job.windowId).catch(() => {});
    for (const j of this.jobs.values()) {
      if (j.mode === "waiting" && j.host === job.host) this.startOrQueue(j);
    }
  },

  onWindowRemoved(wid) {
    for (const job of this.jobs.values()) {
      if (job.windowId === wid && !job.done) {
        job.windowId = null;
        this.fail(job, "the resolver window was closed");
      }
    }
  },
};
