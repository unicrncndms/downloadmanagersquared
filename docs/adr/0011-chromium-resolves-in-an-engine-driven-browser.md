# Chromium resolves run in a browser the engine drives

Date: 2026-11-18
Status: accepted

## Context

A resolve opens the landing page through one interface, isolated from the user's other cookies, and catches the link the site hands out. Firefox does it with a container per interface and `proxy.onRequest` (`extension/background/resolver.js`). Chrome has neither, no SOCKS credentials, and no `webRequestBlocking` for store installs. The options are in `.scratch/chromium-support/issues/04-isolation-unit.md`.

## Decision

Option D. For a resolve from a Chromium browser, the engine starts its own Chromium (Edge, which every Windows 10/11 has, or the user's Chrome) with its own `--user-data-dir` in the data folder, and drives it over the DevTools protocol. Each interface gets one `Target.createBrowserContext({ proxyServer: "socks5://127.0.0.1:<port>" })`: its own cookies and its own exit, like a container. No extension runs in that browser; the replay script is injected.

Rejected:

- **A. A profile per interface.** Each profile needs the extension from the store; Chrome 137 dropped `--load-extension`.
- **B. One global proxy.** The user's own browsing would go out through the resolving interface, resolves would be serial, and a dead proxy would fall back to direct.
- **E. Policy install.** Gives capture-before-body only, not routing or isolation, and marks the browser "managed by your organization".
- **F. Keep resolves in Firefox.** Chrome users would need a Firefox-family browser for per-IP links.

## Consequences

- Resolver windows of a Chrome user are windows of that separate browser, not of their Chrome. A captcha is solved there.
- The download is caught at its response headers (`Fetch.requestPaused`, response stage) and dropped, as Firefox's `onHeadersReceived` does: no download starts. The site has sent the headers by then, so a link spent by its first request is spent in both browsers (`.scratch/chromium-support/issues/18-cdp-spike.md`).
- The browser is driven over `--remote-debugging-pipe` (handles passed with `--remote-debugging-io-pipes`), so no other process can connect to it.
- Recipes recorded there are kept by the engine, not in a browser's extension storage.
- Firefox keeps its container resolver unchanged.
