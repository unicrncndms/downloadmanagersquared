"use strict";

// Magnet links never reach the network, so webRequest can't see them: catch the
// click instead and hand the link to DMS². The background gives it back to
// Firefox if the engine can't take it. Firefox no longer opens ftp:// links, so
// those are caught the same way.
document.addEventListener("click", (e) => {
  // Only the user's own click: a page's a.click() would make the engine join its swarm unasked.
  if (!e.isTrusted || e.button !== 0 || e.defaultPrevented) return;
  const a = e.target instanceof Element && e.target.closest("a[href]");
  const kind = a && (/^magnet:/i.test(a.href) ? "magnet" : /^ftps?:/i.test(a.href) ? "ftp" : "");
  if (!kind) return;
  e.preventDefault();
  e.stopPropagation();
  browser.runtime.sendMessage({ type: kind, url: a.href }).catch(() => {});
}, true);
