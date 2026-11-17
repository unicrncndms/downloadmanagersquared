"use strict";
// Gives Chromium the Firefox `browser` namespace (MV3 chrome.* returns promises). Loaded first, Chromium build only.
globalThis.browser ??= globalThis.chrome;
