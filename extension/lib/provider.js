"use strict";

// The VPN provider choice used to be kept in browser.storage; the engine keeps
// it now, so the Downloads window can show it. Moved once, after upgrading:
// sent if the engine has none, then removed. An engine that doesn't keep it
// yet (it isn't in the settings it answers with) leaves it stored for later.
async function handOverProvider(storage, engineSettings, call) {
  const { vpnProvider } = await storage.get("vpnProvider");
  if (vpnProvider === undefined) return;
  if (vpnProvider && !(engineSettings && engineSettings.vpnProvider)) {
    const s = await call("setSettings", { vpnProvider });
    if (!s || s.vpnProvider !== vpnProvider) return;
  }
  await storage.remove("vpnProvider");
}

if (typeof module !== "undefined") module.exports = { handOverProvider };
