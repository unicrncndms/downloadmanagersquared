# downloadmanagersquared (DMS²)

Downloads files over **all your network connections at once** (Ethernet, Wi‑Fi, a phone hotspot, …), so their speeds add up.

- Works with sites that give each IP address its own download link: DMS² opens the site once per connection and takes each connection's link.
- HTTP(S) downloads and torrents, optionally through a WireGuard VPN per connection.
- Windows 10/11 with Firefox, LibreWolf, Waterfox or Floorp.

## Install

1. Download `downloadmanagersquared-windows.zip` from the [latest release](https://github.com/unicrncndms/downloadmanagersquared/releases/latest) and extract it.
2. Run `dms-engine.exe`. SmartScreen warns because the program isn't code-signed: **More info → Run anyway**.
3. Pick the install folder (Program Files by default) and shortcuts, then **Install**. Windows asks for administrator rights only if the folder needs them.
4. Click **Add to …** for your browser and confirm.

DMS² then runs in the tray. The extension asks to enable containers, which it uses to open sites through one connection at a time.

## Using it

Download a file of 100 MB or more as usual: DMS² takes it over, along with magnet links and `.torrent` files. The toolbar icon shows your downloads; **Connections & settings** is at the bottom of that list.

**Sites with a link per IP:** the first time, a window opens with a blue bar. Click through the site until the download starts. DMS² remembers those clicks and repeats them by itself for each connection from then on. These windows start without cookies, so sites that need you logged in can't be clicked through.

Unfinished files end in `.dms.part`. Progress is saved every few seconds, so downloads survive crashes, sleep and restarts.

## Settings

- **Connections:** turn each on or off, set its parallel requests (4) and speed limit, and choose the main connection, used alone for servers that can't split a file. VPN adapters start off.
- **Downloads:** takeover size (100 MB), save folder, downloads at once (1).
- **Torrents:** seed ratio (1).
- **Startup:** start with Windows.
- **Learned sites:** the clicks remembered per site. *Forget* makes DMS² ask again.

### VPN

Each connection can get its own WireGuard tunnel, for downloads, torrents or both. Choose whether they use the bare connections, the tunnels too, or only the tunnels. The tunnels run inside DMS², so no admin rights are needed and other programs aren't affected.

- **Private Internet Access:** sign in, and every connection without its own `.conf` gets a tunnel.
- **Other providers:** import a `.conf` per connection. It must be plain WireGuard with an IPv4 address, one config per connection (a separate device or key at your provider). Obfuscated AmneziaWG configs don't work.
- For sites with a link per IP, use a different server for each connection, or they may share one address.

### Several browsers

Settings and the download list live in DMS² itself, so every browser shares them. Learned sites are kept per browser, and a download is always clicked through in the browser it came from.

## Upgrade and uninstall

**Upgrade:** run `dms-engine.exe` from a newer zip. It updates the existing install and keeps everything. The extension updates itself.

**Uninstall:** *Settings → Apps → Installed apps → downloadmanagersquared*. Downloaded files are always kept; you choose whether settings and the download list go. Then remove the extension in your browser.

## Privacy and security

DMS² has no server and collects nothing; see [PRIVACY.md](PRIVACY.md). It only accepts requests from your own Windows user, and its proxies only reach the internet, never your local network. It doesn't protect you from programs running as you, or from the PC's administrators.

## Limitations

- IPv4 only.
- Replayed clicks are synthetic: sites that only accept real clicks ask every time.
- Links a site creates through a form POST can't be fetched again.
- Servers that fingerprint TLS may refuse DMS² despite your browser's User-Agent.

## Development

```
browser extension ──native messaging──> dms-host.exe ──named pipe──> dms-engine.exe (tray)
  UI, takes over downloads                 (relay)                     downloads, queue, torrents,
  per-IP links in containers ───────── container traffic ──────>     one SOCKS5 proxy per connection
```

- **Engine** (`engine/`, Go): pins sockets to each connection and splits files into chunks. A connection that finishes takes over the far end of the slowest chunk, so faster connections carry more and nothing is fetched twice.
- **Extension** (`extension/`, plain JavaScript, no build step): takes over downloads, holds the UI, and opens sites in a temporary container per connection, routed through that connection's proxy.

Building needs Go (`winget install GoLang.Go`), and Node.js for the extension's tests:

```bash
powershell -ExecutionPolicy Bypass -File build.ps1
```

This runs the tests and writes the exes, an unsigned `.xpi` and the release zip to `dist\`. Release Firefox only accepts signed extensions; load an unsigned one under `about:debugging` → *This Firefox* → *Load Temporary Add-on*.

Tests alone: `cd engine && go test ./...` and `node --test "extension/test/*.test.js"`. `extension/dev/` has a harness for the popup and settings page (`python extension/dev/serve.py 8765`) and a test site for per-IP links (`python extension/dev/landing.py 8780`).

Data and log live in `%APPDATA%\downloadmanagersquared\` (`settings.json`, `downloads\`, `torrents\`, `engine.log`). Unreadable files are renamed to `….broken`, with a notification.

## Licence

MIT, see [LICENSE](LICENSE). The zip's `THIRD-PARTY-NOTICES` lists the engine's libraries and their licences.
