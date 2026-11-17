"use strict";
// Build-time only (not shipped): derives the Chromium MV3 manifest from manifest.json and stages the Chromium copy. Usage: node chromium.js <staged dir> <version>
const fs = require("fs");
const path = require("path");

// The Chrome Web Store item's public key (dashboard, Package tab): fixes the extension ID of an unpacked build (install.go ChromiumExtensionID).
const KEY = "MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA3+6VoGeKL9v9YFClbUmphT3vQXeVY9Cb8yCb2ruNTjXiys8Vr1paPiXu4aYDcwrgKaCXTJlBC+G/9Xr8kx4cxdSYwQsRniVjM2pkTubTtcnUGe2ROeFvOv6qgWk68E1ZLxrNN/zT/Ua3VZyQOsTph7tE7CDPpYw30vBAq80hA8qiJvIZfkOoKCq5H+eSb7s9wmTF94/GIzlOHK34neJzs/IW6/89m+TePJ28JAQTFgfFNdl9c3AZws3LEgzHpXvPOWag+3mSPoRQ32QXKVeViObLOSZz2Jl1weyxJLkdM8HU2xIGmrig20LScYU58G5daLcfamzdA2kwLrOvYojxOQIDAQAB";
const SHIM = "lib/browser-shim.js";
// Chrome can't resolve per-IP links yet, so its description must not claim it. Also the store summary (docs/store-listing.md).
const DESCRIPTION = "Download big files over all your network connections at once. Needs the free Windows engine from GitHub.";
const DROP = ["contextualIdentities", "proxy", "webRequestBlocking", "theme", "menus"];

// store: the Chrome Web Store copy, without `key` (the store refuses it and signs with its own).
function chromiumManifest(ff, store = false) {
  const m = { ...ff, manifest_version: 3, ...(store ? {} : { key: KEY }) };
  m.description = DESCRIPTION;
  delete m.browser_specific_settings;
  delete m.browser_action;
  const png = (sizes) => Object.fromEntries(sizes.map((n) => [n, "icons/icon-" + n + ".png"]));
  m.action = { ...ff.browser_action, default_icon: png([16, 32]) };
  m.icons = png([16, 32, 48, 128]);
  m.background = { service_worker: "background/sw.js" };
  m.permissions = ff.permissions.filter((p) => p !== "<all_urls>" && !DROP.includes(p));
  m.host_permissions = ["<all_urls>"];
  m.content_scripts = ff.content_scripts.map((c) => ({ ...c, js: [SHIM, ...c.js] }));
  return m;
}

// The pages also run in the engine's WebView2 window, so the shim goes into the Chromium copy only.
function withShim(html) {
  return html.replace("<head>", '<head>\n  <script src="../' + SHIM + '"></script>');
}

// The popup's height fix (ui/chromium.css), after the page's last stylesheet so it wins. The popup only.
function chromiumPage(name, html) {
  if (name !== "popup.html") return html;
  const links = [...html.matchAll(/<link rel="stylesheet"[^>]*>/g)];
  const last = links[links.length - 1];
  const at = last.index + last[0].length;
  return html.slice(0, at) + '\n  <link rel="stylesheet" href="chromium.css">' + html.slice(at);
}

if (require.main === module) {
  const [dir, version, mode] = process.argv.slice(2);
  const ff = JSON.parse(fs.readFileSync(path.join(__dirname, "..", "manifest.json"), "utf8"));
  const m = chromiumManifest(ff, mode === "store");
  m.version = version;
  fs.writeFileSync(path.join(dir, "manifest.json"), JSON.stringify(m, null, 2));
  const ui = path.join(dir, "ui");
  for (const f of fs.readdirSync(ui).filter((f) => f.endsWith(".html"))) {
    const p = path.join(ui, f);
    fs.writeFileSync(p, chromiumPage(f, withShim(fs.readFileSync(p, "utf8"))));
  }
}

module.exports = { chromiumManifest, withShim, chromiumPage, KEY };
