"use strict";

// Where a DMS² container's traffic may go. Pure, no browser.*: the resolver's
// proxy and cancel listeners both ask it, and the Node tests require it.

// The engine's SOCKS port for the container's interface, or 0 when there is
// none to use (engine not connected, interface unknown or absent): then the
// request must not leave at all.
function containerPort(iface, connected, interfaces) {
  const v = connected && (interfaces || []).find((i) => i.id === iface);
  return v && v.present && v.socksPort > 0 ? v.socksPort : 0;
}

if (typeof module !== "undefined") module.exports = { containerPort };
