"use strict";

// Gets a fresh download link for one interface: the landing page is opened in
// a container whose traffic goes through the engine's SOCKS proxy for that
// interface, the remembered clicks are replayed, and the download the site
// starts is caught and handed to the engine instead.
//
// Job state is plain data, kept in storage.session where there is one, and
// every timeout is a deadline in it: a background that restarts mid-job (an
// MV3 service worker) picks its jobs up again. A "learn" job only records the
// recipe (the engine's Record recipe button) and reports no link.
const Resolver = {
  containers: {},   // ifaceId -> cookieStoreId
  removing: new Map(), // ifaceId -> the remove of its container in flight
  ifaceByStore: {}, // cookieStoreId -> ifaceId
  recipesSeen: 0,   // "recipes" events so far: an answer older than the last one is stale
  recipes: {},      // landing-page host -> recipe (lib/recipe.js): the engine's, which keeps every browser's; a copy in storage.local
  jobs: new Map(),  // "downloadId|ifaceId" -> job
  tabJobs: new Map(), // tabId -> job: the job's own tabs and the ones they opened
  newTabs: new Map(),  // tabId -> its judge (watchNewTab), for a tab a replayed click opened
  creating: new Map(),
  loaded: false,    // jobs, containers and recipes are read back
  ready: null,      // settles once they are
  wake: 0,          // the timeout of the nearest deadline
  saving: false,

  COLORS: ["blue", "turquoise", "green", "yellow", "orange", "red", "pink", "purple"],
  PREFIX: "DMS² · ", // every DMS² container's name starts so

  // Registers every listener before anything is awaited (an MV3 worker only
  // gets the event that woke it if they are), then reads the state back.
  init() {
    // The recipes are the engine's (it keeps every browser's): kept in step here for replays and the settings page.
    Engine.on("recipes", (r) => { this.recipesSeen++; this.setRecipes(r); });
    Engine.on("connection", ({ connected }) => { if (connected) this.syncRecipes().catch(() => {}); });
    // A resolve needs a proxy per request and containers: without them (Chromium) none is ever sent, so nothing is registered.
    if (!browser.proxy?.onRequest || !browser.contextualIdentities) {
      this.loaded = true;
      return (this.ready = Promise.resolve());
    }
    // They start registered: until the state is read back nobody knows whether
    // a container is left. Once it is, fence() drops them if none is.
    this.fence();
    // Without the engine no resolve can finish: fail them and close their windows.
    Engine.on("connection", ({ connected }) => {
      if (!connected) for (const job of [...this.jobs.values()]) this.fail(job, "the engine went away");
    });
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
    Engine.on("resolve", (r) => this.ready.then(() => this.request(r)).catch((e) => console.error(e)));
    this.ready = this.load();
    return this.ready;
  },

  // The proxy listener and the blocking cancel listener, for every URL: they
  // cost every request the browser makes, so they exist only while a job or a
  // DMS² container does (or the state is unread). Fail closed wins over speed:
  // a container left by a failed cleanup keeps them. The cancel listener is the
  // second fence: with no proxy to send it through, a container request is
  // cancelled (Firefox would otherwise fail a dead extension proxy over to the
  // user's own manual or PAC proxy). Sync, so no container request slips
  // through before they are there.
  fence() {
    const want = !this.loaded || this.needsState();
    const all = { urls: ["<all_urls>"] };
    this.onProxy = this.onProxy || ((d) => this.proxyFor(d));
    this.onCancel = this.onCancel || ((d) => this.cancelUnrouted(d));
    for (const [ev, f, ...args] of [
      [browser.proxy.onRequest, this.onProxy, all],
      [browser.webRequest.onBeforeRequest, this.onCancel, all, ["blocking"]],
    ]) {
      if (want && !ev.hasListener(f)) ev.addListener(f, ...args);
      else if (!want && ev.hasListener(f)) ev.removeListener(f);
    }
    if (typeof syncWatch === "function") syncWatch();
  },

  // The background needs the engine's state (the ports) while a resolve or a
  // container exists, popup open or not.
  needsState() {
    return this.jobs.size > 0 || Object.keys(this.containers).length > 0;
  },

  get session() {
    return browser.storage.session;
  },

  async load() {
    const s = await browser.storage.local.get(["containers", "recipes"]);
    this.recipes = s.recipes || {};
    // Jobs whose window is still open carry on (a restarted background); the
    // others went with their window.
    let saved = [];
    try { saved = (this.session && (await this.session.get("resolverJobs")).resolverJobs) || []; } catch (e) { /* none */ }
    for (const job of Array.isArray(saved) ? saved : []) {
      if (!job || job.done || this.jobs.has(job.key)) continue;
      if (job.windowId !== null && !(await browser.windows.get(job.windowId).catch(() => null))) continue;
      this.jobs.set(job.key, job);
      for (const t of job.tabs) this.tabJobs.set(t, job);
    }
    // Containers live only while a resolve runs: any left from before (a crash,
    // a quit mid-resolve) go, found by stored id and by name, except those of
    // jobs carrying on.
    const keep = {};
    for (const job of this.jobs.values()) {
      const store = (s.containers || {})[job.iface];
      if (store && job.mode !== "waiting") keep[job.iface] = store;
    }
    const all = await browser.contextualIdentities.query({}).catch(() => []);
    const stale = staleContainers(s.containers, Object.values(keep), all, this.PREFIX);
    // One that will not go stays in containers, fenced, and cleanup retries it.
    const stuck = {};
    for (const store of stale) await browser.contextualIdentities.remove(store).catch(() => { stuck[`stale:${store}`] = store; });
    this.containers = { ...keep, ...stuck, ...this.containers };
    this.saveContainers();
    this.loaded = true;
    this.fence();
    this.recheck(); // deadlines that passed while the background was away
  },

  // Writes the jobs to storage.session, once per turn of the event loop.
  save() {
    if (!this.session || this.saving) return;
    this.saving = true;
    Promise.resolve().then(() => {
      this.saving = false;
      this.session.set({ resolverJobs: [...this.jobs.values()] }).catch(() => {});
    });
  },

  request(r) {
    const key = `${r.id}|${r.iface}`;
    const running = this.jobs.get(key);
    if (running) {
      // The link expired while its recipe was being recorded: the recording's
      // link is now wanted after all.
      if (!r.learn && running.learn) {
        running.learn = false;
        this.save();
      }
      return;
    }
    const job = {
      key, id: r.id, iface: r.iface, ifaceName: r.ifaceName || r.iface, pageUrl: r.pageUrl,
      socksPort: r.socksPort || 0, // the proxy port the engine named for this job
      cookieStore: r.cookieStore || "", // where the download came from: its login is copied into the container
      host: hostOf(r.pageUrl), mode: "new", step: 0, recordFrom: 0, recorded: [], learn: !!r.learn,
      tabs: [], tabId: null, windowId: null, loading: false, skipped: false,
      deadlines: {},    // name -> { at, action, args }: what to do when, see setTimer
      clicked: null,    // during replay: { index, at, clicks, effects, mark } of the step waiting for an effect
      mark: 0,          // the page's load or the previous click: a step's wait counts from here
      lastClick: 0,     // the download's wait counts from here
      measured: { steps: [] }, // the waits this replay saw, saved when it succeeds
      budgetFor: -1,    // the step whose budget runs
      retried: [],      // steps that had their second chance in a restored window
      activity: 0,      // while a person is needed: the last click, page load or captcha change
    };
    this.jobs.set(key, job);
    this.fence(); // before launch awaits anything
    quiet(this.startOrQueue(job));
    this.save();
  },

  startOrQueue(job) {
    const choice = startChoice(job, !!this.recipes[job.host], this.jobs.values());
    if (choice === "waiting") {
      job.mode = "waiting"; // first visit to this site (one job records, the others wait), or the site's window cap is reached
      return;
    }
    if (choice !== "endLearn") job.mode = choice; // now, so jobs starting in this same turn count it
    return choice === "endLearn" ? this.endLearn(job) : this.launch(job, choice);
  },

  // Without an explicit size Firefox gives these windows odd shapes (very wide and
  // short), both when opening one and when restoring one that started minimized.
  shownBounds() {
    const width = Math.min(1280, Math.round(screen.availWidth * 0.8));
    const height = Math.min(1000, Math.round(screen.availHeight * 0.85));
    return {
      state: "normal", width, height,
      left: (screen.availLeft || 0) + Math.round((screen.availWidth - width) / 2),
      top: (screen.availTop || 0) + Math.round((screen.availHeight - height) / 2),
    };
  },

  // Brings a minimized window out, once. A window already shown is the
  // person's: it isn't moved, sized or raised again (a captcha breaks under that).
  async show(job, extra) {
    try {
      const win = await browser.windows.get(job.windowId);
      if (win.state !== "minimized") return;
      await browser.windows.update(job.windowId, { state: "normal" });
      await browser.windows.update(job.windowId, { ...this.shownBounds(), focused: true, ...extra });
    } catch (e) { /* window already closed */ }
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
    job.measured = { steps: [] };
    job.budgetFor = -1;
    const visible = mode === "record";
    // The page opens with the user's login for its site, not as a stranger.
    await copyCookies(job.pageUrl, job.cookieStore || "firefox-default", store);
    let win;
    try {
      win = await browser.windows.create({ url: job.pageUrl, cookieStoreId: store, ...(visible ? this.shownBounds() : { state: "minimized" }) });
    } catch (e) {
      return this.fail(job, "could not open the page: " + e.message);
    }
    if (!win.tabs || !win.tabs[0]) return this.fail(job, "could not open the page");
    job.windowId = win.id;
    job.tabId = win.tabs[0].id;
    this.addTab(job, job.tabId);
    if (visible) {
      notify("Show DMS² how to get the link",
        `Click through ${job.host} for ${job.ifaceName} as you normally would. Your clicks are remembered for next time.`);
    }
    if (visible) this.touch(job);
    else this.setTimer(job, "overall", replayTimeout(this.recipes[job.host]), "fail", "timed out");
  },

  addTab(job, tabId) {
    if (!job.tabs.includes(tabId)) job.tabs.push(tabId);
    this.tabJobs.set(tabId, job);
  },

  // Something happened while a person is needed (a click, a page load, a
  // captcha coming or going): they get another five minutes.
  touch(job, why = "nobody clicked through the page in time") {
    job.activity = Date.now();
    this.setTimer(job, "overall", RECORD_IDLE, "idleOut", why);
  },

  idleOut(job, why) {
    if (recordingIdle(job.activity, Date.now())) this.fail(job, why);
  },

  // A step that ran out of time in a minimized window gets one more go with the
  // window shown but not focused: some pages only count down while visible.
  // False once that has been tried, or if the window isn't minimized.
  async secondChance(job, index) {
    if (job.retried.includes(index)) return false;
    const win = await browser.windows.get(job.windowId).catch(() => null);
    if (!win || win.state !== "minimized") return false;
    job.retried.push(index);
    await browser.windows.update(job.windowId, { ...this.shownBounds(), focused: false }).catch(() => {});
    this.setTimer(job, "overall", replayTimeout(this.recipes[job.host]), "fail", "timed out");
    return true;
  },

  // A step's budget ran out: once more if the window was minimized, then ask for help.
  async budgetOut(job, index, ms, why) {
    if (job.done || job.mode !== "replay") return;
    if (await this.secondChance(job, index)) {
      this.setTimer(job, "budget", ms, "budgetOut", index, ms, why);
      if (index < this.steps(job).length) this.sendStep(job);
      return;
    }
    this.switchToRecord(job, index, why);
  },

  steps(job) {
    return (this.recipes[job.host] && this.recipes[job.host].steps) || [];
  },

  // A tab opened from a job's tab, or in its window, is the job's too.
  onTabCreated(tab) {
    const owner = this.ownerOf({ tabId: tab.openerTabId, windowId: tab.windowId });
    const job = owner && owner.job;
    if (!job || job.done) return;
    this.addTab(job, tab.id);
    if (job.mode === "replay") this.watchNewTab(job, tab);
    this.save();
  },

  // A tab a replayed click opened: the same site carries on there; another
  // site gets a moment to start a download, else it is a pop-up ad and closes.
  // Judged when the tab changes (onTabUpdated) and once when the moment is up.
  watchNewTab(job, tab) {
    const openedAt = Date.now();
    let timer;
    const check = async () => {
      clearTimeout(timer);
      if (job.done) return this.newTabs.delete(tab.id);
      const t = await browser.tabs.get(tab.id).catch(() => null);
      if (!this.newTabs.has(tab.id)) return; // judged meanwhile
      if (!t) return this.newTabs.delete(tab.id);
      const url = t.url && t.url !== "about:blank" ? t.url : t.pendingUrl || "";
      const age = Date.now() - openedAt;
      const verdict = popupVerdict({ openerSite: siteOf(job.lastUrl || job.pageUrl), site: url ? siteOf(url) : "", age, download: false });
      if (verdict === "wait") {
        timer = setTimeout(check, POPUP_GRACE - age + 1);
        return;
      }
      this.newTabs.delete(tab.id);
      if (verdict === "keep") {
        // The next page opened in a tab of its own: follow it there.
        job.tabId = tab.id;
        this.clickEffect(job, "newTab");
      } else {
        browser.tabs.remove(tab.id).catch(() => {});
      }
    };
    this.newTabs.set(tab.id, check);
    check();
  },

  onTabUpdated(tabId, info, tab) {
    const watch = this.newTabs.get(tabId);
    if (watch) watch();
    const job = this.tabJobs.get(tabId);
    if (!job || job.done || tabId !== job.tabId) return;
    if (info.status === "loading") {
      job.loading = true;
      this.clickEffect(job, "navigation");
    }
    if (info.status === "complete") {
      job.loading = false;
      job.lastUrl = tab.url;
      job.mark = Date.now();
      if (job.mode === "record") this.touch(job);
      // A new page may mean the captcha is behind us, or be the challenge
      // reloading itself: done only if the new page reports none in time.
      // Meanwhile the window stays as it is.
      if (job.captcha) this.setTimer(job, "captchaGone", CAPTCHA_SETTLE, "captchaGone");
      this.drive(job);
    }
    this.save();
  },

  async drive(job) {
    try {
      await browser.tabs.executeScript(job.tabId, { file: "/lib/ready.js", runAt: "document_idle" });
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
  // a page that only worked for the first connection, and on to the next one in
  // case it started too early (a recipe that starts on a listing only fits one item).
  async sendRecord(job) {
    const pages = Capture.pagesById[job.id] || [];
    const tab = await browser.tabs.get(job.tabId).catch(() => null);
    let i = tab ? pages.indexOf(tab.url) : -1;
    if (i < 0) i = pages.indexOf(job.pageUrl);
    // Back only within the site: the page before it is often a search engine.
    const prev = i > 0 && siteOf(pages[i - 1]) === siteOf(pages[i]) ? pages[i - 1] : null;
    const next = i >= 0 && i + 1 < pages.length ? pages[i + 1] : null;
    const { n, of } = learningPlace([...this.jobs.values()], job);
    const tokens = accentTokens(pageAccent(Engine.settings)) || accentTokens("#1d4ed8");
    browser.tabs.sendMessage(job.tabId, { cmd: "record", ifaceName: job.ifaceName, prev, next, n, of, learn: job.learn, tokens }).catch(() => {});
  },

  // A captcha needs a person: show the window and hold all timeouts until it's solved.
  captchaShown(job) {
    if (job.captcha) return this.clearTimer(job, "captchaGone"); // the page after a reload has it still
    job.captcha = true;
    this.clearTimer(job, "budget");
    this.clearTimer(job, "effect");
    job.clicked = null;
    this.touch(job, "the captcha wasn't solved in time");
    this.show(job, { drawAttention: true });
    notify("DMS² needs you to solve a captcha", `${job.host} asks for a captcha on ${job.ifaceName}. The window is open; DMS² continues once it's solved.`);
  },

  captchaGone(job) {
    if (job.captcha) this.captchaDone(job);
  },

  captchaDone(job) {
    this.clearTimer(job, "captchaGone");
    job.captcha = false;
    if (job.mode !== "replay") {
      this.touch(job);
      return;
    }
    // Back out of the way; the page's script carries on with the next click by itself.
    browser.windows.update(job.windowId, { state: "minimized" }).catch(() => {});
    this.setTimer(job, "overall", replayTimeout(this.recipes[job.host]), "fail", "timed out");
    job.budgetFor = -1; // the step gets its budget afresh
    this.sendStep(job);
  },

  // Sends the step to click, with its budget: twice its recorded wait plus 15 s
  // to be clicked, counted from the page load or the previous click.
  sendStep(job) {
    const recipe = this.recipes[job.host] || {};
    const steps = this.steps(job);
    if (job.captcha) return; // continued once the captcha is solved
    if (job.step >= steps.length) {
      // All clicks done: the site's timer runs, then the download should start.
      this.armBudget(job, steps.length, budget(recipe.downloadWait), "The download didn't start");
      return;
    }
    const ms = budget(steps[job.step].wait);
    this.armBudget(job, job.step, ms + 1000, "The page didn't let DMS² click in time"); // the page reports first
    browser.tabs.sendMessage(job.tabId, { cmd: "replay", step: steps[job.step], index: job.step, next: steps[job.step + 1], budget: ms }).catch(() => {});
  },

  // Starts the budget of step index (steps.length: the download) unless it runs already.
  armBudget(job, index, ms, why) {
    if (job.budgetFor === index) return;
    job.budgetFor = index;
    this.setTimer(job, "budget", ms, "budgetOut", index, ms, why);
  },

  // A replayed click showed an effect: the step is done. After a navigation
  // the next step is sent once the new page has loaded.
  clickEffect(job, kind) {
    const c = job.clicked;
    if (!c || job.mode !== "replay") return;
    c.effects[kind] = true;
    if (afterClick(c.effects, Date.now() - c.at, c.clicks) !== "done") return;
    job.clicked = null;
    this.clearTimer(job, "effect");
    job.measured.steps[c.index] = c.at - c.mark;
    job.mark = c.at;
    job.step = c.index + 1;
    if (kind !== "navigation" && !job.loading) this.sendStep(job);
  },

  // No effect within the wait: click again, or after too many clicks ask the user.
  judgeClick(job) {
    const c = job.clicked;
    if (!c || job.done || job.mode !== "replay") return;
    const verdict = afterClick(c.effects, Date.now() - c.at, c.clicks);
    if (verdict === "wait") {
      this.setTimer(job, "effect", Math.max(1, EFFECT_WAIT - (Date.now() - c.at)), "judgeClick"); // once, for what is left
    } else if (verdict === "retry") {
      this.sendStep(job);
    } else if (verdict === "giveUp") {
      job.clicked = null;
      this.switchToRecord(job, c.index, "Clicking the button had no effect");
    }
  },

  switchToRecord(job, fromIndex, why) {
    if (job.done) return;
    job.mode = "record";
    job.recordFrom = fromIndex;
    job.recorded = [];
    this.clearTimers(job);
    this.show(job);
    notify("DMS² needs a hand", `${why} on ${job.host} (${job.ifaceName}). Please click through; your clicks will be remembered.`);
    this.sendRecord(job);
    this.touch(job);
  },

  // Called by Capture for responses ownerOf gives a job: the download the
  // site starts is the link. Downloads in a finished job's tab go nowhere.
  onHeaders(d, cookie, owner) {
    const h = headerMap(d.responseHeaders);
    if (d.statusCode >= 300 || isPage(h) || !looksLikeDownload(h)) return {};
    const size = responseSize(h, d.statusCode);
    if (!isAttachment(h) && size >= 0 && size < (1 << 20)) return {};
    if (!owner.job) return {};
    quiet(this.finish(owner.job, d.url, cookie));
    return { cancel: true };
  },

  async onDownload(item, owner) {
    await discardDownload(item.id);
    if (owner.job) await quiet(this.finish(owner.job, item.finalUrl || item.url));
  },

  // cookie: the header sent with the download's request, if one passed by.
  async finish(job, url, cookie) {
    if (job.done) return;
    job.done = true;
    this.clearTimers(job);
    if (job.startUrl && job.startUrl !== job.pageUrl && isHttp(job.startUrl)) this.correctPage(job);
    if (job.mode === "record") this.saveRecipe(job);
    // Where the first click was is where to start on this site from now on.
    if (job.mode === "record" && job.recordFrom === 0 && job.startUrl) Capture.learnStepBack(job.id, job.startUrl);
    if (job.mode === "replay" && this.recipes[job.host]) {
      // The site's waits this time are the ones to expect next time.
      job.measured.download = Date.now() - job.lastClick;
      this.putRecipe(job.host, withMeasuredWaits(this.recipes[job.host], job.measured));
    }
    // A learn job's link is thrown away: the download keeps its own.
    if (job.learn) return this.endLearn(job);
    if (cookie === undefined) cookie = await cookieHeader(url, this.containers[job.iface]);
    try {
      await Engine.call("resolved", { id: job.id, iface: job.iface, url, cookie, referer: job.lastUrl || job.pageUrl });
    } catch (e) {
      console.warn("DMS² could not report link:", e);
    }
    this.cleanup(job);
  },

  fail(job, error) {
    if (job.learn) return this.endLearn(job); // nothing changes for the download
    if (job.done) return;
    job.done = true;
    this.clearTimers(job);
    Engine.call("resolveFailed", { id: job.id, iface: job.iface, error }).catch(() => {});
    this.cleanup(job);
  },

  // A learn job is over, recipe saved or not: the engine may offer the button again.
  endLearn(job) {
    const first = !job.ended;
    job.done = job.ended = true;
    this.clearTimers(job);
    if (first) Engine.call("learnEnded", { id: job.id, iface: job.iface }).catch(() => {});
    if (this.jobs.get(job.key) === job) this.cleanup(job);
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
    const downloadWait = job.lastClick ? Date.now() - job.lastClick : 0;
    this.putRecipe(job.host, recorded(this.recipes[job.host], job.recordFrom, job.recorded, downloadWait, Date.now()));
  },

  // A recipe recorded or re-timed here: kept now, and sent to the engine, which keeps every browser's.
  putRecipe(host, recipe) {
    this.recipes[host] = recipe;
    browser.storage.local.set({ recipes: this.recipes });
    if (Engine.connected) Engine.call("setRecipe", { host, recipe }).catch(() => {});
  },

  forgetRecipe(host) {
    delete this.recipes[host];
    browser.storage.local.set({ recipes: this.recipes });
    return Engine.call("forgetRecipe", { host });
  },

  // The engine's recipes, as it sends them on every change.
  setRecipes(recipes) {
    this.recipes = recipes && typeof recipes === "object" ? recipes : {};
    browser.storage.local.set({ recipes: this.recipes });
  },

  // On connect: this browser's recipes from before the engine kept them go to
  // it once (the newer per site stays), then the engine's list is the one used.
  async syncRecipes() {
    await this.ready;
    const s = await browser.storage.local.get(["recipes", "recipesMoved"]);
    if (!s.recipesMoved && s.recipes && Object.keys(s.recipes).length) await Engine.call("mergeRecipes", { recipes: s.recipes });
    await browser.storage.local.set({ recipesMoved: true });
    const seen = this.recipesSeen;
    const recipes = await Engine.call("recipes");
    // As an event, so the open settings pages get them now (Chromium has no copy of its own to show before).
    if (seen === this.recipesSeen) Engine.emit("recipes", recipes); // else an event since is newer
  },

  cleanup(job) {
    for (const t of job.tabs) this.tabJobs.delete(t);
    this.jobs.delete(job.key);
    if (job.windowId !== null) browser.windows.remove(job.windowId).catch(() => {});
    // The interface's container goes with its last running resolve, and its
    // tabs with it: nothing of it is left for session restore.
    this.sweep();
    this.fence();
    for (const j of [...this.jobs.values()]) {
      if (j.mode === "waiting" && j.host === job.host) quiet(this.startOrQueue(j));
    }
    this.save();
  },

  // Removes the containers no running resolve uses. A container leaves
  // this.containers only once its remove has resolved: until then it stays
  // fenced, and a failed remove is tried again at the next sweep.
  sweep() {
    for (const [iface, store] of Object.entries(this.containers)) {
      if (this.removing.has(iface) || [...this.jobs.values()].some((j) => j.iface === iface && j.mode !== "waiting")) continue;
      this.removing.set(iface, browser.contextualIdentities.remove(store).then(() => {
        if (this.containers[iface] === store) {
          delete this.containers[iface];
          this.saveContainers();
        }
      }, (e) => console.warn("DMS² container not removed:", e && e.message))
        .finally(() => this.removing.delete(iface)));
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
