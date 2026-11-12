"use strict";

// The palette a connection is shown in (ui.css --c0 …), by the engine's index.
const IFACE_COLORS = ["Purple", "Teal", "Pink", "Brown", "Green", "Grey"];

// A tunnel's path ID is its interface's, with this suffix (engine paths.Tunnel).
const TUNNEL_SUFFIX = "~vpn";

// ifaceHue is a connection's colour on the pages: the one the engine keeps for
// it. A tunnel is a line of its own, so it gets a colour no interface uses (the
// n-th free one for the n-th interface); with none free it shares its
// interface's. An engine that sends none gets the old rule, the connection's
// place in the list.
function ifaceHue(id, state) {
  const list = (state && state.interfaces) || [];
  const n = IFACE_COLORS.length;
  const tunnel = id.endsWith(TUNNEL_SUFFIX);
  const base = tunnel ? id.slice(0, -TUNNEL_SUFFIX.length) : id;
  const own = list.find((x) => x.id === base);
  if (own && Number.isInteger(own.color)) {
    if (tunnel) {
      const taken = new Set(list.filter((x) => !x.tunnel && Number.isInteger(x.color)).map((x) => x.color % n));
      const free = [...Array(n).keys()].filter((c) => !taken.has(c));
      const place = list.filter((x) => !x.tunnel).findIndex((x) => x.id === base);
      if (free.length) return `var(--c${free[place % free.length]})`;
    }
    return `var(--c${own.color % n})`;
  }
  return `var(--c${Math.max(list.findIndex((x) => x.id === id), 0) % IFACE_COLORS.length})`;
}

if (typeof module !== "undefined") module.exports = { ifaceHue, IFACE_COLORS };
