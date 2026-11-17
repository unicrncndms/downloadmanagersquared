"use strict";
// Checks the generated Chromium MV3 manifest, the service-worker import list and the fixed extension ID.
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("fs");
const path = require("path");
const crypto = require("crypto");
const { chromiumManifest, withShim, chromiumPage, KEY } = require("../dev/chromium.js");

const ext = path.join(__dirname, "..");
const ff = JSON.parse(fs.readFileSync(path.join(ext, "manifest.json"), "utf8"));
const m = chromiumManifest(ff);

test("MV3 keys, no Firefox-only permissions", () => {
  assert.equal(m.manifest_version, 3);
  assert.equal(m.background.service_worker, "background/sw.js");
  assert.ok(m.action && !m.browser_action);
  assert.deepEqual(m.host_permissions, ["<all_urls>"]);
  assert.ok(m.permissions.includes("contextMenus"));
  for (const p of ["<all_urls>", "menus", "contextualIdentities", "proxy", "webRequestBlocking", "theme", "scripting"]) {
    assert.ok(!m.permissions.includes(p), p);
  }
  assert.equal(m.browser_specific_settings, undefined);
  assert.equal(ff.manifest_version, 2);
});

test("icons are PNGs that exist", () => {
  const paths = [...Object.values(m.icons), ...Object.values(m.action.default_icon)];
  assert.equal(paths.length, 6);
  for (const p of paths) assert.ok(p.endsWith(".png") && fs.existsSync(path.join(ext, p)), p);
});

test("sw.js imports the shim, then background.scripts in order", () => {
  const sw = fs.readFileSync(path.join(ext, "background/sw.js"), "utf8");
  const got = [...sw.matchAll(/^\s*"([^"]+)",?$/gm)].map((x) => path.posix.normalize("background/" + x[1]));
  assert.deepEqual(got, ["lib/browser-shim.js", ...ff.background.scripts]);
});

test("shim comes first in content scripts and in pages", () => {
  for (const c of m.content_scripts) assert.equal(c.js[0], "lib/browser-shim.js");
  assert.match(withShim("<head>\n<script src=\"a.js\">"), /<head>\s*<script src="..\/lib\/browser-shim.js">/);
  for (const f of fs.readdirSync(path.join(ext, "ui")).filter((f) => f.endsWith(".html"))) {
    assert.ok(fs.readFileSync(path.join(ext, "ui", f), "utf8").includes("<head>"), f);
    assert.ok(!fs.readFileSync(path.join(ext, "ui", f), "utf8").includes("browser-shim"), f);
  }
});

test("ID from key equals ChromiumExtensionID in install.go", () => {
  const hex = crypto.createHash("sha256").update(Buffer.from(KEY, "base64")).digest("hex").slice(0, 32);
  const id = [...hex].map((c) => String.fromCharCode(97 + parseInt(c, 16))).join("");
  const go = fs.readFileSync(path.join(ext, "../engine/internal/install/install.go"), "utf8");
  assert.equal(go.match(/ChromiumExtensionID = "([a-p]{32})"/)[1], id);
});

test("the popup gets the Chromium popup fix after its last stylesheet; other pages don't", () => {
  const read = (f) => fs.readFileSync(path.join(ext, "ui", f), "utf8");
  const popup = chromiumPage("popup.html", read("popup.html"));
  const link = '<link rel="stylesheet" href="chromium.css">';
  assert.equal(popup.split(link).length - 1, 1);
  assert.ok(popup.indexOf(link) > popup.indexOf('href="popup.css"'));
  assert.ok(!popup.slice(popup.indexOf(link) + link.length).includes('rel="stylesheet"'));
  for (const f of ["settings.html", "welcome.html"]) {
    assert.equal(chromiumPage(f, read(f)), read(f), f);
  }
  assert.ok(fs.existsSync(path.join(ext, "ui", "chromium.css")));
});
