"use strict";

// The pages' accent color. Pure, no browser.* or DOM: the pages and the
// background load it as a script and the Node tests require it.
//
// One color, "#rrggbb", becomes --accent, --accent-soft and --on-accent for
// the light and the dark pages. A color too pale (light) or too dark (dark) to
// read as text on the page is moved toward black or white until it reads
// (WCAG AA, 4.5:1), never refused.

const ACCENT_PRESETS = [
  ["Blue", "#1d4ed8"], ["Violet", "#7c3aed"], ["Pink", "#db2777"], ["Red", "#dc2626"],
  ["Orange", "#ea580c"], ["Green", "#16a34a"], ["Teal", "#0d9488"], ["Graphite", "#475569"],
];

// The panels the accent is read against (ui.css --panel).
const LIGHT_PANEL = [255, 255, 255];
const DARK_PANEL = [29, 32, 38];
const AA = 4.6; // 4.5, with room for rounding to whole bytes

// parseColor reads "#rgb", "#rrggbb", "rgb(r, g, b)" / "rgba(…)" or [r, g, b]
// (Firefox theme colors come in all of these), else null.
function parseColor(c) {
  if (Array.isArray(c) && c.length >= 3) return c.slice(0, 3).map((v) => clamp(Math.round(Number(v))));
  if (typeof c !== "string") return null;
  const s = c.trim().toLowerCase();
  let m = s.match(/^#([0-9a-f]{3})$/);
  if (m) return [...m[1]].map((h) => parseInt(h + h, 16));
  m = s.match(/^#([0-9a-f]{6})([0-9a-f]{2})?$/);
  if (m) return [0, 2, 4].map((i) => parseInt(m[1].slice(i, i + 2), 16));
  m = s.match(/^rgba?\(\s*(\d+(?:\.\d+)?)[\s,]+(\d+(?:\.\d+)?)[\s,]+(\d+(?:\.\d+)?)/);
  if (m) return [m[1], m[2], m[3]].map((v) => clamp(Math.round(Number(v))));
  return null;
}

const clamp = (v) => Math.min(255, Math.max(0, Number.isFinite(v) ? v : 0));
const hex = (rgb) => "#" + rgb.map((v) => clamp(Math.round(v)).toString(16).padStart(2, "0")).join("");
const mix = (a, b, t) => a.map((v, i) => v + (b[i] - v) * t); // t of the way from a to b

function luminance(rgb) {
  const [r, g, b] = rgb.map((v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrast(a, b) {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
}

// tokens for one scheme: the accent, moved toward `to` until it reads as
// text on the panel and on its own soft tint, and the text on the accent.
function tokens(c, panel, to, soft) {
  let accent = to, accentSoft = mix(panel, to, soft);
  for (let t = 0; t <= 1; t += 0.02) {
    const a = mix(c, to, t), s = mix(panel, a, soft);
    if (contrast(a, panel) >= AA && contrast(a, s) >= AA) { accent = a; accentSoft = s; break; }
  }
  const white = [255, 255, 255], ink = [11, 16, 32];
  const onAccent = contrast(white, accent) >= contrast(ink, accent) ? white : ink;
  return { accent: hex(accent), accentSoft: hex(accentSoft), onAccent: hex(onAccent) };
}

// accentTokens is { light, dark }, each { accent, accentSoft, onAccent }, or
// null for a color that can't be read.
function accentTokens(color) {
  const c = parseColor(color);
  if (!c) return null;
  return { light: tokens(c, LIGHT_PANEL, [0, 0, 0], 0.1), dark: tokens(c, DARK_PANEL, [255, 255, 255], 0.16) };
}

// accentCSS is the stylesheet that sets the tokens, or "" to keep ui.css's.
function accentCSS(color) {
  const t = accentTokens(color);
  if (!t) return "";
  const vars = (s) => `--accent: ${s.accent}; --accent-soft: ${s.accentSoft}; --on-accent: ${s.onAccent};`;
  return `:root { ${vars(t.light)} }\n@media (prefers-color-scheme: dark) { :root { ${vars(t.dark)} } }\n`;
}

// pageAccent is the color the pages use: the one chosen, else Firefox's
// theme's, else none (ui.css's blue).
function pageAccent(settings) {
  return (settings && (settings.accent || settings.themeAccent)) || "";
}

// themeAccent picks the accent of a Firefox theme (browser.theme.getCurrent()):
// the selected tab's line, else the highlight colors, as "#rrggbb"; "" for a
// theme that sets none (the default themes).
function themeAccent(theme) {
  const colors = (theme && theme.colors) || {};
  for (const k of ["tab_line", "toolbar_field_highlight", "popup_highlight", "icons_attention", "tab_loading"]) {
    const c = parseColor(colors[k]);
    if (c) return hex(c);
  }
  return "";
}

if (typeof module !== "undefined") {
  module.exports = { ACCENT_PRESETS, parseColor, contrast, accentTokens, accentCSS, pageAccent, themeAccent };
}
