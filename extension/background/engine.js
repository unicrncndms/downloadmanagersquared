"use strict";

// Connection to the native engine through the dms-host native-messaging shim.
const TOO_LONG = "The downloads list is too long for Firefox to show, so this one is out of date. Clear finished downloads in the DMS² window (its tray icon).";
const Engine = {
  port: null,
  connected: false,
  lastError: "",
  notInstalled: false, // the browser found no native host: the engine was never installed
  version: "",         // the engine's, from hello
  settings: null,
  state: null,
  autostart: false,
  rpc: rpcClient((m) => Engine.port.postMessage(m), (f, ms) => setTimeout(f, ms)),
  join: chunkJoiner(),
  listeners: new Map(),
  retryDelay: 2000,
  retryCap: 30000,     // 3 s while the welcome page waits for the engine (fast)
  quit: false,         // the user quit the engine from its tray: don't start it again by reconnecting

  // start connects again after the user quit the engine, which starts it.
  start() {
    if (this.port) return;
    clearTimeout(this.retryTimer); // a retry already pending would open a second port
    this.quit = false;
    this.retryDelay = 2000;
    this.connect();
  },

  // fast(true) retries within seconds, for as long as the welcome page is open.
  fast(on) {
    this.retryCap = on ? 3000 : 30000;
    if (!on || this.port || this.quit) return;
    clearTimeout(this.retryTimer);
    this.retryDelay = Math.min(this.retryDelay, this.retryCap);
    this.retryTimer = setTimeout(() => this.connect(), 1000);
  },

  connect() {
    if (this.port) return;
    const port = browser.runtime.connectNative("downloadmanagersquared");
    this.port = port;
    this.join = chunkJoiner(); // the new host numbers its lines from 1 again
    port.onMessage.addListener((msg) => this.onMessage(msg));
    const lost = (msg) => {
      if (this.port !== port) return;
      this.port = null;
      this.rpc.drop("engine disconnected");
      this.notInstalled = /no such native application/i.test(msg);
      this.setConnected(false, this.quit ? "" : msg || this.lastError || "engine disconnected");
      if (this.quit) return;
      this.retryTimer = setTimeout(() => this.connect(), this.retryDelay);
      this.retryDelay = Math.min(this.retryDelay * 2, this.retryCap);
    };
    port.onDisconnect.addListener((p) => lost((p.error && p.error.message) || ""));
    // A resolve needs a proxied hidden window with its own container: only some browsers can.
    const resolves = !!(browser.proxy?.onRequest && browser.contextualIdentities);
    this.call("hello", { userAgent: navigator.userAgent, version: browser.runtime.getManifest().version, resolves })
      .then((h) => {
        this.settings = h.settings;
        this.state = h.state;
        this.autostart = h.autostart;
        this.version = h.version || "";
        this.notInstalled = false;
        this.retryDelay = 2000;
        this.setConnected(true, "");
      })
      // No answer to hello leaves the port open but useless: start over.
      // (Our own disconnect() fires no onDisconnect.)
      .catch((e) => { if (this.port === port) { port.disconnect(); lost(e.message); } });
  },

  onMessage(msg) {
    if (msg.chunk !== undefined) { // a line over 1 MB, cut by dms-host
      msg = this.join(msg);
      if (!msg) return;
    }
    if (msg.event) {
      if (msg.event === "engineUnavailable") {
        this.lastError = "engine could not start: " + msg.data;
        return;
      }
      if (msg.event === "tooLong") {
        // dms-host dropped a message Firefox wouldn't take (over 1 MB): in
        // practice the state, with thousands of downloads listed.
        if (this.connected && this.lastError !== TOO_LONG) this.setConnected(true, TOO_LONG);
        return;
      }
      if (msg.event === "quit") this.quit = true;
      if (msg.event === "state") {
        this.state = msg.data;
        if (this.lastError === TOO_LONG) this.lastError = "";
      }
      if (msg.event === "settings") this.settings = msg.data;
      this.emit(msg.event, msg.data);
      return;
    }
    this.rpc.answer(msg);
  },

  call(method, params) {
    if (!this.port) return Promise.reject(new Error("engine not connected"));
    return this.rpc.call(method, params);
  },

  // Returns a function that removes the listener.
  on(event, fn) {
    if (!this.listeners.has(event)) this.listeners.set(event, new Set());
    this.listeners.get(event).add(fn);
    return () => this.listeners.get(event).delete(fn);
  },

  emit(event, data) {
    for (const fn of this.listeners.get(event) || []) {
      try { fn(data); } catch (e) { console.error(e); }
    }
  },

  setConnected(connected, error) {
    this.connected = connected;
    this.lastError = error;
    this.emit("connection", { connected, error });
  },
};
