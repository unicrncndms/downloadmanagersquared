"use strict";

// resolver-content.js and lib/ready.js use no browser API but runtime.sendMessage and runtime.onMessage: the engine's Chromium resolver runs them with only those two (engine/internal/resolver/script.go).

const test = require("node:test");
const assert = require("node:assert");
const fs = require("fs");
const path = require("path");

test("the resolver page script needs only runtime.sendMessage and runtime.onMessage", () => {
  for (const f of ["resolver-content.js", "lib/ready.js"]) {
    const src = fs.readFileSync(path.join(__dirname, "..", f), "utf8");
    const used = new Set(src.match(/\b(browser|chrome)\.[\w.]+/g) || []);
    for (const u of used) {
      assert.ok(["browser.runtime.sendMessage", "browser.runtime.onMessage.addListener"].includes(u), `${f} uses ${u}`);
    }
  }
});
