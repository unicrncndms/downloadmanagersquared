"use strict";

// The palette a connection is shown in (ui.css --c0 …), by the engine's index.
const IFACE_COLORS = ["Purple", "Teal", "Pink", "Brown", "Green", "Grey"];

// A tunnel's path ID is its interface's, with this suffix (engine paths.Tunnel).
const TUNNEL_SUFFIX = "~vpn";

// ifaceHue is a connection's colour on the pages: the one the engine keeps for
// it. A tunnel is a line of its own with its own kept colour (the engine gives
// it one no connection uses while one is free), so it stays put when
// connections come and go; a tunnel no longer listed takes its interface's. An
// engine that sends no colour gets the old rule, the connection's place in the list.
function ifaceHue(id, state) {
  const list = (state && state.interfaces) || [];
  const n = IFACE_COLORS.length;
  const tunnel = id.endsWith(TUNNEL_SUFFIX);
  const own = list.find((x) => x.id === id) || (tunnel && list.find((x) => x.id === id.slice(0, -TUNNEL_SUFFIX.length)));
  if (own && Number.isInteger(own.color)) return `var(--c${own.color % n})`;
  return `var(--c${Math.max(list.findIndex((x) => x.id === id), 0) % n})`;
}

if (typeof module !== "undefined") module.exports = { ifaceHue, IFACE_COLORS };
