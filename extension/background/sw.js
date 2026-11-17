"use strict";
// Chromium MV3 service-worker entry: the shim, then the same scripts as background.scripts in manifest.json (a test keeps the lists equal).
importScripts(
  "../lib/browser-shim.js",
  "../lib/capture-rules.js",
  "../lib/recipe.js",
  "../lib/ready.js",
  "../lib/route.js",
  "../lib/provider.js",
  "../lib/accent.js",
  "../lib/rpc.js",
  "../lib/chunks.js",
  "../lib/list.js",
  "util.js",
  "engine.js",
  "resolver.js",
  "resolver-routing.js",
  "resolver-timers.js",
  "resolver-messages.js",
  "capture.js",
  "main.js"
);
