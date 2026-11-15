"use strict";

// The resolver's containers and proxy routing: which interface a DMS² container
// goes through, who owns a request, and the fence that cancels what has no port.

Object.assign(Resolver, {
  reindex() {
    this.ifaceByStore = {};
    for (const [iface, store] of Object.entries(this.containers)) this.ifaceByStore[store] = iface;
  },

  saveContainers() {
    this.reindex();
    browser.storage.local.set({ containers: this.containers });
    this.fence();
  },

  async ensureContainer(iface, name) {
    if (this.containers[iface]) return this.containers[iface];
    if (!this.creating.has(iface)) {
      const color = this.COLORS[Object.keys(this.containers).length % this.COLORS.length];
      this.creating.set(iface, browser.contextualIdentities
        .create({ name: `${this.PREFIX}${name || iface}`, color, icon: "circle" })
        .then((c) => {
          this.containers[iface] = c.cookieStoreId;
          this.saveContainers();
          return c.cookieStoreId;
        })
        .finally(() => this.creating.delete(iface)));
    }
    return this.creating.get(iface);
  },

  // The engine's proxy port for an interface, 0 if it has none to use now.
  // Both the proxy and the cancel listener decide by this.
  portFor(iface) {
    return containerPort(iface, Engine.connected, Engine.state && Engine.state.interfaces);
  },

  // Which resolver job a request, download or tab belongs to, from what it
  // carries ({ tabId, windowId, cookieStoreId }): its tab (or the tab that
  // opened it), its window, then, in Firefox only, its container. Returns
  // { job, iface } (job null: a DMS² container with no resolve running), or
  // null when it is the user's own. Proxying and capture both decide by this.
  ownerOf(d) {
    return ownerOf(this, d);
  },

  proxyFor(d) {
    if (!this.loaded) return this.ready.then(() => this.proxyFor(d));
    const owner = this.ownerOf(d);
    // The user's own: {type: "direct"}, which leaves their own proxy in
    // charge. It must never become null: only null forces a request direct,
    // past the user's proxy.
    if (!owner) return { type: "direct" };
    // Fail closed: a resolver page must never leave through the wrong
    // connection. Port 1 is a second fence behind the cancel listener.
    return { type: "socks", host: "127.0.0.1", port: this.portFor(owner.iface) || 1, proxyDNS: true };
  },

  cancelUnrouted(d) {
    if (!this.loaded) return this.ready.then(() => this.cancelUnrouted(d));
    const owner = this.ownerOf(d);
    return owner && !this.portFor(owner.iface) ? { cancel: true } : {};
  }
});
