# Notes for AMO reviewers

downloadmanagersquared (DMS²) is the Firefox half of a Windows download manager. The other half is a native app, the engine (`dms-engine.exe`, with the native-messaging shim `dms-host.exe`). The engine downloads one file over several network connections at once (Ethernet, Wi-Fi, a tethered phone, VPN tunnels). The extension hands it the downloads it should take. Where a site gives each IP address its own download link, the extension also fetches one link per connection.

Both halves are MIT-licensed and live in one repository: `extension/` (this add-on) and `engine/` (Go). The add-on's source is the code as written: no bundler, minifier or transpiler. `build.ps1` only copies `extension/` and writes the version into `manifest.json`.

## What the add-on does

1. **Capture.** A blocking `webRequest.onHeadersReceived` listener looks at top-level and frame responses. A download larger than the user's threshold (100 MB by default), a `.torrent` or a Metalink (`.meta4`) file is cancelled in Firefox and sent to the engine. Downloads that bypass `webRequest` are caught on `downloads.onCreated`. A content script catches clicks on `magnet:` links, which never reach the network. If the engine can't take a download, it goes back to Firefox through `downloads.download`.
2. **Resolve.** For each connection, the engine asks (a `resolve` event) for a link fetched over that connection. The add-on opens the download's landing page in a temporary DMS² container proxied through that connection (see Proxy use), replays the clicks the user once recorded on that site, catches the download the site starts, hands the link to the engine and closes the window. With no recorded clicks yet, or when a replay gets stuck (a captcha, a changed page), it shows the window and asks the user in a notification to click through; those clicks are recorded. The popup's **Record recipe** button starts the same recording on purpose, while the user is there. Resolve jobs are kept in `storage.session`, so they survive a background-page restart.
3. **UI.** The popup and the settings page show the engine's downloads and settings. Every action there is a call to the engine.

## Proxy use

- `proxy.onRequest` affects only requests from the add-on's own containers, named `DMS² · <connection>`. Every other request gets `{type: "direct"}`, which leaves the user's own proxy settings in charge (`background/resolver.js`, `proxyFor`).
- A container request goes to `{type: "socks", host: "127.0.0.1", port: <the engine's port for that connection>, proxyDNS: true}`. The engine opens one SOCKS5 listener per connection, on loopback only.
- **No proxy authentication.** The add-on sends no credentials and handles no `onAuthRequired`. The engine accepts a connection only from a process of the same Windows user, and connects only to public internet addresses (never private, loopback, link-local or CGNAT), so a container page can't reach the user's LAN.
- **No requests of its own.** The add-on makes no `fetch`, `XMLHttpRequest` or WebSocket requests. Container traffic is only what the opened site loads.
- **Cancelled when the engine is gone.** A blocking `webRequest.onBeforeRequest` listener cancels every container request when the engine has no proxy for it, since Firefox would otherwise fall back to the user's own proxy. On disconnect, running resolves fail and their windows close. Containers are removed when their last resolve ends, and leftovers from a crash when the add-on starts.

## What goes to the native app

Messages go over `runtime.connectNative("downloadmanagersquared")` to `dms-host.exe`, which relays them over a named pipe only the same Windows user can open. Nothing goes anywhere else.

- `hello`: the browser's User-Agent. The engine answers with its version, settings and state. The state can carry `update` (a newer release's version and GitHub release page), which the engine learns by asking `api.github.com` once a day; the popup shows it as a notice with a link, and nothing is downloaded.
- `add`: a download's URL, file name, the page it was started on, its Referer, the User-Agent, and the `Cookie` header Firefox sent for it. With no request to read (context menu, `downloads.onCreated`), the cookies Firefox would send for that URL in that container, filtered to the download's own site.
- `resolved` / `resolveFailed` / `setPageUrl` / `learnEnded`: a link fetched for one connection (URL, the temporary container's cookies, Referer), or why it failed, the landing page the add-on found, and the end of a recording.
- `recipeHosts`: the host names the add-on has recorded clicks for, so the engine knows when to offer **Record recipe**. The clicks themselves stay in the add-on.
- Calls from the popup and settings page: pause, resume, remove, reorder, settings changes and the like, including a VPN login or WireGuard config the user enters there. The add-on doesn't store those.

The engine keeps a download's cookies only until it completes, and sends them only to that site. See `PRIVACY.md`.

## Permissions

| Permission | Why |
| --- | --- |
| `nativeMessaging` | The only channel to the engine. |
| `downloads` | Cancel and erase a download the engine took over. Catch downloads `webRequest` doesn't see. Give one back to Firefox when the engine can't take it. |
| `webRequest`, `webRequestBlocking` | Read response headers to recognise large downloads and cancel them (blocking). Read the `Cookie` and `Referer` request headers to hand along. Cancel container requests when the engine is gone (blocking). |
| `webNavigation` | Track which pages a tab showed, including cache and in-page navigation, to find a download's landing page. |
| `proxy` | Route the add-on's own containers through the engine's per-connection SOCKS proxies. Nothing else is proxied. |
| `contextualIdentities` | Create, find and remove the temporary DMS² containers, one per connection. |
| `cookies` | Read a URL's cookies in its container when no request header is available, and the containers' cookies. |
| `tabs` | Open and close resolve windows, inject the replay script with `tabs.executeScript`, and read the tabs open at startup for their pages. |
| `storage` | The recorded clicks per site, the pages that led to recent downloads, the container ids, and running resolve jobs (`storage.session`). All local. |
| `contextMenus` | "Download with DMS²" on links and pages. |
| `notifications` | Say that DMS² took a download or torrent, that it couldn't (Firefox downloads it instead), or that a resolve window needs the user. |
| `<all_urls>` | Downloads and landing pages can be on any site: the `webRequest` listeners, script injection into resolve windows, and the `magnet:` content script. |

## Data collection (`data_collection_permissions`)

All three are required: `browsingActivity` (pages that led to a download), `websiteActivity` (recorded clicks) and `websiteContent` (download URLs, cookies). The data goes only to the native app on the same PC. The project has no server.

## Trying it

1. Download `downloadmanagersquared-windows.zip` from the GitHub release and run `dms-engine.exe` on Windows 10 or 11. Setup installs the engine, registers the native host and starts the engine in the tray. Its **Add to …** step installs the GitHub build of the add-on; skip it if you install this one.
2. Install the add-on. The popup lists the PC's network connections.
3. Download any file larger than the threshold (lower it in the settings page to test with a small one). The engine takes it over and the popup shows it running.

Without the engine, the popup says "Engine not installed" and the add-on does nothing else.
