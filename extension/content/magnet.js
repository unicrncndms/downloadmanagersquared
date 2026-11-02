"use strict";

// Magnet links never reach the network, so webRequest can't see them: catch the
// click instead and hand the link to DMS². The background gives it back to
// Firefox if the engine can't take it.
document.addEventListener("click", (e) => {
  if (e.button !== 0 || e.defaultPrevented) return;
  const a = e.target instanceof Element && e.target.closest("a[href]");
  if (!a || !/^magnet:/i.test(a.href)) return;
  e.preventDefault();
  e.stopPropagation();
  browser.runtime.sendMessage({ type: "magnet", url: a.href }).catch(() => {});
}, true);
