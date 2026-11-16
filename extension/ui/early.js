"use strict";

// The first thing in <body>: says whether the page is in the Downloads window
// (the engine sets dmsWindow; the harness its own Host) before anything is
// drawn, so the header never draws as the popup's first and then moves.
if (window.dmsWindow || (window.Host && window.Host.openDownloads)) document.body.classList.add("windowed", "engine-window");
