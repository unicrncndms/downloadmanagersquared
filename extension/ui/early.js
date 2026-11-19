"use strict";

// The first thing in <body>: says whether the page is in the Downloads window
// (the engine sets dmsWindow; the harness its own Host) before anything is
// drawn, so the header never draws as the popup's first and then moves.
if (window.dmsWindow || (window.Host && window.Host.openDownloads)) document.body.classList.add("windowed", "engine-window");
// The theme last chosen (common.js applyTheme keeps it), before the first paint: Firefox
// colours its popup's frame from the page as it loads, and a dark frame stayed around a
// light page when the theme came only with the engine's settings.
try {
  const t = JSON.parse(localStorage.getItem("dms-theme") || "{}");
  if (t.theme) document.documentElement.dataset.theme = t.theme;
  if (t.tint) document.documentElement.toggleAttribute("data-tint", true);
} catch (e) { /* no storage: the system's theme until the settings come */ }
