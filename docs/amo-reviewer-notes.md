# Notes for AMO reviewers

downloadmanagersquared (DMS²) is the Firefox half of a Windows download manager. The other half is a native app, the engine (`dms-engine.exe`, with the native-messaging shim `dms-host.exe`). The engine downloads one file over several network connections at once (Ethernet, Wi-Fi, a tethered phone, VPN tunnels). The extension hands it the downloads it should take. Where a site gives each IP address its own download link, the extension also fetches one link per connection.

Both halves are MIT-licensed and live in one repository: `extension/` (this add-on) and `engine/` (Go). The add-on's source is the code as written. There is no bundler, minifier or transpiler. `build.ps1` only copies `extension/` and writes the version into `manifest.json`.

## What the add-on does

1. **Capture.** A `webRequest.onHeadersReceived` listener looks at top-level and frame responses. If one is a download larger than the threshold the user set (100 MB by default), or a `.torrent` file, the add-on cancels it in Firefox and sends it to the engine instead. Downloads that bypass `webRequest` are caught on `downloads.onCreated`. A content script catches clicks on `magnet:` links, since those never reach the network. If the engine can't take a download, it goes back to Firefox through `downloads.download`.
2. **Resolve.** For each connection the engine uses, it asks the add-on (a `resolve` event) for a link fetched over that connection. The add-on opens the download's landing page in a temporary DMS² container whose traffic is proxied through that connection (see Proxy use). It then replays the clicks the user once recorded on that site and catches the download the site starts. The add-on hands the link to the engine and closes the window. When a site has no recorded clicks yet, or a replay gets stuck (a captcha, a changed page), the add-on shows the window and asks the user, in a notification, to click through. Those clicks are what it records.
3. **UI.** The popup and the settings page show the engine's downloads and settings. Every action there is a call to the engine.

## Proxy use

- `proxy.onRequest` affects only requests from the add-on's own containers: those named `DMS² · <connection>`. Every other request gets `{type: "direct"}`, which leaves the user's own proxy settings in charge (`extension/background/resolver.js`, `proxyFor`).
- A container request goes to `{type: "socks", host: "127.0.0.1", port: <the engine's port for that connection>, proxyDNS: true}`. The engine opens one SOCKS5 listener per connection, on loopback only (`engine/internal/socks`).
- **No proxy authentication.** The add-on sends no credentials and handles no `onAuthRequired`. The engine checks instead: it accepts a connection only if the client process belongs to the same Windows user. It also connects only to public internet addresses, never to private, loopback, link-local or CGNAT ones, so a page in a DMS² container can't reach the user's LAN through it.
- **No requests of its own.** The add-on makes no `fetch`, `XMLHttpRequest` or WebSocket requests. Container traffic is only what the opened site itself loads.
- **Cancelled when the engine is gone.** A blocking `webRequest.onBeforeRequest` listener cancels every container request when the engine has no proxy to offer (engine not connected, connection unknown or gone). Firefox would otherwise fall back to the user's own proxy. On disconnect, running resolves fail and their windows close. Containers are created when needed and removed when their last resolve ends. Any left over from a crash are removed when the add-on starts.

## What goes to the native app

Messages go over `runtime.connectNative("downloadmanagersquared")` to `dms-host.exe`, which relays them over a named pipe that only the same Windows user can open. Nothing goes anywhere else.

- `hello`: the browser's User-Agent string. The engine answers with its version, settings and state.
- `add`: a download's URL, file name, the page it was started on, its Referer, the User-Agent, and the `Cookie` header Firefox sent for that request. If there was no request (context menu, `downloads.onCreated`), the cookies Firefox would send for that URL in that container go instead, filtered to the download's own first-party domain.
- `resolved` / `resolveFailed` / `setPageUrl`: a link fetched for one connection (URL, cookies of the temporary container, Referer), or why it failed, and the landing page the add-on found.
- Calls from the popup and settings page: pause, resume, remove, settings changes and the like.

The engine keeps a download's cookies only until that download completes, and sends them only to that site. See `PRIVACY.md`.

## Permissions

| Permission | Why |
| --- | --- |
| `nativeMessaging` | The only channel to the engine. |
| `downloads` | Cancel and erase a download the engine took over. Catch downloads `webRequest` doesn't see. Give a download back to Firefox when the engine can't take it. |
| `webRequest`, `webRequestBlocking` | Read response headers to recognise large downloads and cancel them in Firefox (blocking). Read the `Cookie` and `Referer` request headers to hand along. Cancel container requests when the engine is gone (blocking). |
| `webNavigation` | Track which pages a tab showed, including cache and in-page navigation, to find a download's landing page. |
| `proxy` | Route the add-on's own containers through the engine's per-connection SOCKS proxies. Nothing else is proxied. |
| `contextualIdentities` | Create, find and remove the temporary DMS² containers, one per connection. |
| `cookies` | Read a URL's cookies in its container when no request header is available. Read the containers' cookies. |
| `tabs` | Open and close resolve windows. Inject the replay script into them with `tabs.executeScript`. Read the tabs that were already open when the add-on starts, for their pages. |
| `storage` | Keep the recorded clicks per site, the pages that led to recent downloads, and the container ids. All of it stays local. |
| `contextMenus` | "Download with DMS²" on links and pages. |
| `notifications` | Tell the user that DMS² took a download or a torrent, that it couldn't (and Firefox downloads it instead), or that a resolve window needs them (first recording, a captcha, a stuck replay). |
| `<all_urls>` | Downloads and landing pages can be on any site. Covers the `webRequest` listeners, script injection into resolve windows, and the `magnet:` content script. |

## Data collection (`data_collection_permissions`)

All three are declared required: `browsingActivity` (pages that led to a download), `websiteActivity` (recorded clicks) and `websiteContent` (download URLs, cookies). The data goes only to the native app on the same PC. Neither the add-on nor the engine sends anything to a server of the project's. There is none.

## Trying it

1. Download `downloadmanagersquared-windows.zip` from the GitHub release and run `dms-engine.exe` on Windows 10 or 11. Setup installs the engine, registers the native host and starts the engine in the tray.
2. Install the add-on. The popup lists the PC's network connections.
3. Download any file larger than the threshold (lower it in the settings page to test with a small one). The engine takes it over and the popup shows it running.

Without the engine, the popup says "Engine not installed" and the add-on does nothing else.
