# downloadmanagersquared (DMS²)

Downloads large files over **all your network connections at once** (Ethernet, Wi‑Fi, phone hotspot, …), with several parallel range requests on each. Works with sites that hand out a **different download link per IP address**: DMS² opens the site once through each connection in Firefox and catches the link the site generates for that IP.

Windows 11 + Firefox only. HTTP(S) only.

## How it fits together

```
Firefox extension ──native messaging──> dms-host.exe ──named pipe──> dms-engine.exe (tray)
   UI (downloads popup + settings page)      (relay)                    downloads, queue, settings
   captures big downloads                                               one SOCKS5 proxy per interface
   resolves per-IP links in containers  ───── container traffic ─────>  (pages load via that interface)
```

- **Engine** (`engine/`, Go): finds the interfaces that have their own internet route and pins sockets to each one (`IP_UNICAST_IF` + source-address bind + that interface's DNS). Splits each file into chunks. When a connection finishes, it takes the far end of whichever chunk would take longest, so faster links carry more and nothing is downloaded twice. Runs in the tray, starts with Windows, and keeps downloading after Firefox closes.
- **Extension** (`extension/`, plain JS, no build step): takes over downloads above the threshold (30 GB by default), adds a right-click "Download with DMS²" option, and holds the UI: the **downloads** live in the toolbar popup, **connections, settings and learned sites** on one settings page (button at the bottom of the popup).
- **Per-IP links**: for each interface, the extension creates a Firefox container whose traffic goes through the engine's proxy for that interface. It opens the landing page there, replays the clicks it learned for that site, waits for the site's timer and catches the download the site starts. The first time on a new site it shows you the page and records your clicks.

## Build

Needs Go (installed with `winget install GoLang.Go`).

```bash
powershell -ExecutionPolicy Bypass -File build.ps1
```

This runs the download, torrent and engine tests and puts `dms-engine.exe`, `dms-host.exe` and `downloadmanagersquared.zip` (the extension) into `dist\`.

## Install

1. Engine (current user, no admin):

   ```bash
   powershell -ExecutionPolicy Bypass -File installer\install.ps1
   ```

   It copies the executables to `%LOCALAPPDATA%\downloadmanagersquared`, registers the native-messaging host for Firefox, enables start-with-Windows and starts the engine in the tray.

2. Extension. Release Firefox only installs signed extensions:
   - **To try it right away**: open `about:debugging#/runtime/this-firefox` → *Load Temporary Add-on…* → pick `extension\manifest.json`. It stays until Firefox restarts.
   - **For daily use**: sign it as an *unlisted* extension (free, private, not published). Upload `dist\downloadmanagersquared.zip` at <https://addons.mozilla.org/developers/addon/submit/distribution> and choose "On your own", then install the signed `.xpi` you get back. Bump `version` in `manifest.json` for each new upload.

The extension asks to enable Firefox containers. DMS² creates one container per connection, named `DMS² · <connection>`.

## First use with a site that binds links to IPs

1. Start the download in Firefox as usual. If the file is over the threshold, DMS² takes it over and starts on the connection you're on.
2. For each other connection, DMS² needs that connection's own link. The first time on a site, a window opens in that connection's container with a blue bar at the bottom: click through as you normally would (your two clicks; then the 5 s timer runs and the download starts). DMS² remembers those clicks.
3. From then on, the other connections are resolved automatically in a minimized window. If the site changes and a remembered click isn't found, the window comes to the front and you click through once more.

The **Learned sites** section of the settings page lists remembered clicks; *Forget* re-teaches a site. In the popup, each download's *Details* shows the landing page DMS² uses. Correct it there if the guess is wrong.

## Settings worth knowing

All of these are on the settings page (**Connections & settings** at the bottom of the popup, or in the tray menu):

- **Connections**: turn connections on or off (metered and VPN connections start off), set parallel connections per interface, and pick the **main connection** (used alone for servers without range support).
- **Settings**: capture threshold, save folder (default: your Windows Downloads folder), downloads at the same time (default 1), the averaging window for time left (default 60 s), and start with Windows.
- **Expected SHA-256 / MD5** (per download, under *Details*): if set, the file is verified when it finishes.

Files are written as `name.dms.part` (sparse, so even a 50 GB file is created instantly) and renamed when done. Progress is saved every few seconds, so downloads resume after a crash, sleep or reboot.

## Files and logs

- Settings and download state: `%APPDATA%\downloadmanagersquared\` (`settings.json`, `downloads\*.json`)
- Engine log: `%APPDATA%\downloadmanagersquared\engine.log`
- Uninstall: `installer\uninstall.ps1` (downloads and settings are kept)

## Known limitations

- **IPv4 only.** Each connection is used through its IPv4 address. Links are fetched over IPv4 as well, so a link and the connection that uses it always match.
- **Replayed clicks are synthetic.** A site that only accepts real mouse clicks will ask you to click every time.
- **Minimized resolver windows**: if a site's countdown only runs while visible, the window comes to the front after 60 s and asks you to click through.
- A download that is still waiting for its links counts against the "downloads at the same time" limit.
- Download links the site creates via a form POST cannot be fetched again by the engine.
- Some servers refuse non-browser clients. The engine sends Firefox's User-Agent for captured downloads, but servers that fingerprint TLS may still reject it.

## Tests

```bash
cd engine && go test ./internal/download/
```

They simulate two interfaces with IP-bound links (including the browser round trip), a link serving a different file, servers without range support, pause and resume across a restart, and servers limiting connections with 403/429. `go test ./internal/netif/` lists this machine's interfaces and fetches the public IP through each one (needs network).
