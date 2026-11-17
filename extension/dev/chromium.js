"use strict";
// Build-time only (not shipped): derives the Chromium MV3 manifest from manifest.json and stages the Chromium copy. Usage: node chromium.js <staged dir> <version>
const fs = require("fs");
const path = require("path");

// Public half of .secrets/chromium-extension.pem: fixes the extension ID (install.go ChromiumExtensionID).
const KEY = "MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAopOGNtrEdRIGnC+IYm6e20lmMR/bVZJ2XMINYIe1M27dY8WxHxiM9d5D0g2l8KfejlCRDoqjk2CbMH9QVjqLQmIqtS9htB7Nkdtq64pUJNXDlJLEvTseoYfOcCYHWlhyFGqKR28Xcunkt5Z4HChUq82VXXklaX+O0DOWTK1jGfF09989gMuUAxkgtiR8OFuKZJrShbTWIk/3p6bfh9weohjg4tjzH0CJ9An3N1Iri/VBmlMyibc+Bm27c5z/Fmm3dk0VNXYtrpvmc+HuT7rUGlyeSRhZros3xcMeHcOHbV7edQtLIysvpACkndnJnQbvPS0QS6u/MDSlFHdN7KeeUwIDAQAB";
const SHIM = "lib/browser-shim.js";
const DROP = ["contextualIdentities", "proxy", "webRequestBlocking", "theme", "menus"];

function chromiumManifest(ff) {
  const m = { ...ff, manifest_version: 3, key: KEY };
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

if (require.main === module) {
  const [dir, version] = process.argv.slice(2);
  const ff = JSON.parse(fs.readFileSync(path.join(__dirname, "..", "manifest.json"), "utf8"));
  const m = chromiumManifest(ff);
  m.version = version;
  fs.writeFileSync(path.join(dir, "manifest.json"), JSON.stringify(m, null, 2));
  const ui = path.join(dir, "ui");
  for (const f of fs.readdirSync(ui).filter((f) => f.endsWith(".html"))) {
    const p = path.join(ui, f);
    fs.writeFileSync(p, withShim(fs.readFileSync(p, "utf8")));
  }
}

module.exports = { chromiumManifest, withShim, KEY };
