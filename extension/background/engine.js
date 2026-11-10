"use strict";

// Connection to the native engine through the dms-host native-messaging shim.
const Engine = {
  port: null,
  connected: false,
  lastError: "",
  notInstalled: false, // the browser found no native host: the engine was never installed
  version: "",         // the engine's, from hello
  settings: null,
  state: null,
  autostart: false,
  nextId: 1,
  pending: new Map(),
  listeners: new Map(),
  retryDelay: 2000,
  quit: false,         // the user quit the engine from its tray: don't start it again by reconnecting

  // start connects again after the user quit the engine, which starts it.
  start() {
    if (this.port) return;
    this.quit = false;
    this.retryDelay = 2000;
    this.connect();
  },

  connect() {
    const port = browser.runtime.connectNative("downloadmanagersquared");
    this.port = port;
    port.onMessage.addListener((msg) => this.onMessage(msg));
    const lost = (msg) => {
      if (this.port !== port) return;
      this.port = null;
      for (const { reject } of this.pending.values()) reject(new Error("engine disconnected"));
      this.pending.clear();
      this.notInstalled = /no such native application/i.test(msg);
      this.setConnected(false, this.quit ? "" : msg || this.lastError || "engine disconnected");
      if (this.quit) return;
      setTimeout(() => this.connect(), this.retryDelay);
      this.retryDelay = Math.min(this.retryDelay * 2, 30000);
    };
    port.onDisconnect.addListener((p) => lost((p.error && p.error.message) || ""));
    this.call("hello", { userAgent: navigator.userAgent })
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
    if (msg.event) {
      if (msg.event === "engineUnavailable") {
        this.lastError = "engine could not start: " + msg.data;
        return;
      }
      if (msg.event === "quit") this.quit = true;
      if (msg.event === "state") this.state = msg.data;
      if (msg.event === "settings") this.settings = msg.data;
      this.emit(msg.event, msg.data);
      return;
    }
    const p = this.pending.get(msg.id);
    if (!p) return;
    this.pending.delete(msg.id);
    if (msg.error) p.reject(new Error(msg.error));
    else p.resolve(msg.result);
  },

  call(method, params) {
    if (!this.port) return Promise.reject(new Error("engine not connected"));
    const id = this.nextId++;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.port.postMessage({ id, method, params: params || {} });
      setTimeout(() => {
        if (this.pending.delete(id)) reject(new Error("engine did not answer"));
      }, 60000);
    });
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
