"use strict";

const test = require("node:test");
const assert = require("node:assert");
const { ACCENT_PRESETS, parseColor, contrast, accentTokens, accentCSS, pageAccent, themeAccent } = require("../lib/accent.js");

const LIGHT_PANEL = [255, 255, 255];
const DARK_PANEL = [29, 32, 38];

test("colors are read in every form Firefox themes use", () => {
  assert.deepEqual(parseColor("#abc"), [170, 187, 204]);
  assert.deepEqual(parseColor("#1D4ED8"), [29, 78, 216]);
  assert.deepEqual(parseColor("#1d4ed880"), [29, 78, 216]);
  assert.deepEqual(parseColor("rgb(10, 20, 30)"), [10, 20, 30]);
  assert.deepEqual(parseColor("rgba(10 20 30 / .5)"), [10, 20, 30]);
  assert.deepEqual(parseColor([300, -1, 7]), [255, 0, 7]);
  assert.equal(parseColor("red"), null);
  assert.equal(parseColor(undefined), null);
});

test("every accent reads as text in light and dark, on the panel and on its soft tint", () => {
  const colors = [...ACCENT_PRESETS.map((p) => p[1]), "#ffffff", "#000000", "#ffff00", "#00ffff", "#111111", "#7fffd4"];
  for (const c of colors) {
    const t = accentTokens(c);
    for (const [s, panel] of [[t.light, LIGHT_PANEL], [t.dark, DARK_PANEL]]) {
      const a = parseColor(s.accent);
      assert.ok(contrast(a, panel) >= 4.5, `${c}: ${s.accent} on its panel`);
      assert.ok(contrast(a, parseColor(s.accentSoft)) >= 4.5, `${c}: ${s.accent} on ${s.accentSoft}`);
      assert.ok(contrast(parseColor(s.onAccent), a) >= 4.5, `${c}: ${s.onAccent} on ${s.accent}`);
    }
  }
});

test("a color that already reads is kept", () => {
  assert.equal(accentTokens("#1d4ed8").light.accent, "#1d4ed8");
  assert.equal(accentTokens("#7c3aed").light.onAccent, "#ffffff");
});

test("the stylesheet sets both schemes, or nothing for no color", () => {
  const css = accentCSS("#16a34a");
  assert.match(css, /^:root \{ --accent: #[0-9a-f]{6}; --accent-soft: #[0-9a-f]{6}; --on-accent: #[0-9a-f]{6}; \}/);
  assert.match(css, /@media \(prefers-color-scheme: dark\)/);
  assert.equal(accentCSS(""), "");
});

test("the chosen color wins over Firefox's, and none leaves the pages' blue", () => {
  assert.equal(pageAccent({ accent: "#dc2626", themeAccent: "#16a34a" }), "#dc2626");
  assert.equal(pageAccent({ accent: "", themeAccent: "#16a34a" }), "#16a34a");
  assert.equal(pageAccent({}), "");
  assert.equal(pageAccent(null), "");
});

test("a Firefox theme's accent is its tab line, else a highlight; the default theme has none", () => {
  assert.equal(themeAccent({ colors: { frame: "#000", tab_line: "rgb(255, 0, 128)" } }), "#ff0080");
  assert.equal(themeAccent({ colors: { tab_line: null, toolbar_field_highlight: "#0a84ff" } }), "#0a84ff");
  assert.equal(themeAccent({ colors: null }), "");
  assert.equal(themeAccent({}), "");
});
