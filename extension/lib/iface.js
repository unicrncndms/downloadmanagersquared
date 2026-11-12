"use strict";

// The palette a connection is shown in (ui.css --c0 …), by the engine's index.
const IFACE_COLORS = ["Purple", "Teal", "Pink", "Brown", "Green", "Grey"];

// A tunnel's path ID is its interface's, with this suffix (engine paths.Tunnel).
const TUNNEL_SUFFIX = "~vpn";

// ifaceHue is a connection's colour on the pages: the one the engine keeps for
// it, which a tunnel shares with its interface. An engine that sends none gets
// the old rule, the connection's place in the list.
function ifaceHue(id, state) {
  const list = (state && state.interfaces) || [];
  const base = id.endsWith(TUNNEL_SUFFIX) ? id.slice(0, -TUNNEL_SUFFIX.length) : id;
  const own = list.find((x) => x.id === base);
  if (own && Number.isInteger(own.color)) return `var(--c${own.color % IFACE_COLORS.length})`;
  return `var(--c${Math.max(list.findIndex((x) => x.id === id), 0) % IFACE_COLORS.length})`;
}

if (typeof module !== "undefined") module.exports = { ifaceHue, IFACE_COLORS };
