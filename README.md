# downloadmanagersquared (DMS²)

**Download one file over all your internet connections at once.** Plug in a phone hotspot or a second line next to your home connection, and DMS² splits each download across them so their speeds add up. A 300 Mbit/s line plus a 100 Mbit/s hotspot downloads at close to 400 Mbit/s.

For Windows 10/11 with Firefox, LibreWolf, Waterfox or Floorp. Free and open source (MIT).

## What it does

- **Adds up your connections.** Each file is split into pieces fetched over every connection in parallel. A faster connection takes over more of the work, nothing is downloaded twice, and a download survives crashes, sleep and restarts.
- **Takes over your browser's downloads.** Download as usual: files over 100 MB, magnet links, `.torrent` and Metalink (`.meta4`) files go to DMS² by themselves.
- **Handles file hosts that hand out a link per IP address.** DMS² opens the site once per connection, repeats the clicks you made the first time, and takes each connection's link.
- **Downloads torrents** over all connections too.
- **Uses mirrors.** A Metalink, or a server that names mirrors with a checksum, lets each connection fetch from its own mirror, so one slow mirror doesn't hold back the rest. A mirror serving another file is dropped, and the finished file is checked against the checksum.
- **Unpacks ZIPs while they download.** With *Download as folder*, each file in the archive is extracted and checked as soon as its part has arrived, and that part of the archive is freed. You end up with the folder, never the `.zip`, and need only about the extracted size in free space instead of archive plus extracted size.
- **A VPN per connection, if you want one.** Each connection can get its own WireGuard tunnel (Private Internet Access, or a `.conf` from your provider). The tunnels run inside DMS²: no admin rights, and other programs aren't affected.
- **Checks files.** Paste a SHA-256 or MD5 and DMS² checks the file when it finishes.

No account, no server, no telemetry: see [PRIVACY.md](PRIVACY.md).

## Will it make my downloads faster?

Only if your connections are really separate lines: your home connection plus a phone hotspot, two internet providers, or a second router on another line. Wi‑Fi and Ethernet to the same router share one line, so together they are no faster than either. And a download can't go faster than the server sends: a server that limits each download to a slow speed limits each connection to it too, though DMS² opens several requests per connection.

## Install

1. Download `downloadmanagersquared-windows.zip` from the [latest release](https://github.com/unicrncndms/downloadmanagersquared/releases/latest) and open it. Extracting it is optional.
2. Run `dms-engine.exe`. If Windows says it might depend on other files in the zip, choose **Run**: it carries what it needs. SmartScreen warns because the program isn't code-signed: **More info → Run anyway**.
3. Pick the install folder (Program Files by default) and shortcuts, then **Install**. Windows asks for administrator rights only if the folder needs them.
4. Click **Add to …** for your browser and confirm.

DMS² then runs in the tray. The extension asks to enable containers, which it uses to open sites through one connection at a time.

## Using it

Download a file of 100 MB or more as usual, or use *Download with DMS²* on a link's menu. The toolbar icon lists your downloads; a left click on the tray icon opens the same list in its own window. Each download shows its progress, speed and the connections it uses; **Details** has the rest. **Connections & settings** is at the bottom of the list.

**Sites with a link per IP:** the first time, a window opens with a blue bar. Click through the site until the download starts. DMS² remembers those clicks and repeats them by itself for each connection from then on. These windows start without cookies, so sites that need you logged in can't be clicked through. Many file hosts limit free downloads per IP address, and their terms may not allow getting around that. Following each host's terms is up to you.

**Download as folder:** tick it when adding a `.zip` link, switch it on in a download's Details, or make it the default in the settings. DMS² fetches the archive's list of files first, then extracts each file once its part is downloaded (files over 1 GB as their bytes arrive), checks it against the archive's CRC, and frees that part of the download. A 100 GB ZIP that extracts to 140 GB needs about 140 GB of space, plus what's still waiting for its file to complete, instead of 240 GB. You can change the folder it extracts into while it runs, on the same drive or another.

- It needs a server that allows split downloads, and ZIPs compressed with Stored, Deflate, Deflate64 or Zstandard. Anything else (encrypted, split archives, other methods) is downloaded as a normal `.zip`, with the reason in Details.
- Each file is checked, but there's no whole archive to check, so a checksum you entered turns the mode off.

**Checksums:** a download's **Details → Checks → Edit** takes a SHA-256 or MD5. DMS² checks the file when it finishes (or right away if it already has), and a download that doesn't match is kept as `.dms.part` instead of finishing. Files from Google Cloud Storage (and sites served from it) carry their MD5, which is checked the same way when you haven't entered one.

Unfinished files end in `.dms.part`. Progress is saved every few seconds.

## Settings

- **Connections:** turn each on or off, set its parallel requests (4) and speed limit, and choose the main connection, used alone for servers that can't split a file. VPN adapters start off.
- **Downloads:** takeover size (100 MB), save folder, downloads at once (1), Download as folder for new ZIPs (off).
- **Torrents:** seed ratio (1).
- **Startup:** start with Windows.
- **Learned sites:** the clicks remembered per site. *Forget* makes DMS² ask again.

### VPN

Each connection can get its own WireGuard tunnel, for downloads, torrents or both. Choose whether they use the bare connections, the tunnels too, or only the tunnels.

- **Private Internet Access:** sign in, and every connection without its own `.conf` gets a tunnel.
- **Other providers:** import a `.conf` per connection. It must be plain WireGuard with an IPv4 address and an IPv4 DNS server (`DNS =`; IPv6 ones aren't used), one config per connection (a separate device or key at your provider). Obfuscated AmneziaWG configs don't work. If the provider forwards ports by NAT-PMP (Proton VPN on P2P servers, with NAT-PMP on when you make the config), DMS² gets a port for torrents by itself (not yet tested with Proton's servers). For a fixed port from your provider's panel (AirVPN), type it in the **Port** box next to the `.conf`.
- For sites with a link per IP, use a different server for each connection, or they may share one address.
- Which providers offer `.conf` files, and why PIA works differently: [docs/vpn-providers.md](docs/vpn-providers.md).

### Several browsers

Settings and the download list live in DMS² itself, so every browser shares them. Learned sites are kept per browser, and a download is always clicked through in the browser it came from.

## Upgrade and uninstall

**Upgrade:** run `dms-engine.exe` from a newer zip. It updates the existing install and keeps everything. The extension updates itself from this repository's releases, signed by Mozilla; to decide yourself, set **Allow automatic updates** to **Off** for it in `about:addons`.

**Uninstall:** *Settings → Apps → Installed apps → downloadmanagersquared*. Downloaded files are always kept; you choose whether settings and the download list go. Then remove the extension in your browser.

## Limitations

- Windows only, and only Firefox-based browsers.
- IPv4 only.
- Replayed clicks are synthetic: sites that only accept real clicks ask every time.
- Links a site creates through a form POST can't be fetched again.
- Servers that fingerprint TLS may refuse DMS² despite your browser's User-Agent.
- A torrent on the bare connections shows its peers the IP address of every connection it uses. That's why DMS² asks about the VPN before such a torrent starts.

## Privacy and security

DMS² has no server and collects nothing; see [PRIVACY.md](PRIVACY.md). It only accepts requests from your own Windows user, and its proxies only reach the internet, never your local network. It doesn't protect you from programs running as you, or from the PC's administrators. To report a vulnerability, see [SECURITY.md](SECURITY.md).

The programs aren't code-signed, but each release's zip, `dms-engine.exe` and `dms-host.exe` carry a GitHub attestation of the workflow run and commit that built them. To check a download, run `gh attestation verify <file> --repo unicrncndms/downloadmanagersquared`.

The extension asks for these permissions:

| Permission | What DMS² uses it for |
|---|---|
| `nativeMessaging` | Talking to the engine through `dms-host.exe` |
| `downloads` | Taking over downloads from the browser, and handing back ones the engine can't take |
| `webRequest`, `webRequestBlocking` | Seeing a download's headers and cookies as it starts, and cancelling any container request that has no connection to go through |
| `webNavigation` | Following which page a download came from (its landing page) |
| `proxy` | Sending each temporary container's traffic through its connection's proxy |
| `contextualIdentities` | Creating the temporary containers that open a site through one connection |
| `cookies` | Reading the cookies a download needs when the browser didn't send any with it, and copying your cookies for a landing page into the temporary container, so the page opens logged in |
| `tabs` | Opening and closing the windows that click through landing pages, recording and replaying the clicks in them, and opening the settings page |
| `storage` | Keeping the learned sites and containers in this browser |
| `contextMenus` | The *Download with DMS²* entries on links and pages |
| `notifications` | Telling you when a download is taken over or can't be, and when a site needs your clicks or a captcha |
| `<all_urls>` | Seeing downloads, magnet links and landing pages on any site |

## Development

```
browser extension ──native messaging──> dms-host.exe ──named pipe──> dms-engine.exe (tray)
  UI, takes over downloads                 (relay)                     downloads, queue, torrents,
  per-IP links in containers ───────── container traffic ──────>     one SOCKS5 proxy per connection
```

- **Engine** (`engine/`, Go): pins sockets to each connection and splits files into chunks. A connection that finishes takes over the far end of the slowest chunk, so faster connections carry more and nothing is fetched twice. For a Download as folder it fetches the ZIP's central directory first and schedules by file (`engine/internal/archive`, [ADR 0006](docs/adr/0006-download-as-folder-frees-the-archive-while-downloading.md)).
- **Extension** (`extension/`, plain JavaScript, no build step): takes over downloads, holds the UI, and opens sites in a temporary container per connection, routed through that connection's proxy. The engine's Downloads window shows the same pages in WebView2.

Building needs Go (`winget install GoLang.Go`), and Node.js for the extension's tests:

```bash
powershell -ExecutionPolicy Bypass -File build.ps1
```

This runs the tests and writes the exes, an unsigned `.xpi` and the release zip to `dist\`. Release Firefox only accepts signed extensions; load an unsigned one under `about:debugging` → *This Firefox* → *Load Temporary Add-on*.

Tests alone: `cd engine && go test ./...` and `node --test "extension/test/*.test.js"`. `extension/dev/` has a harness for the popup and settings page (`python extension/dev/serve.py 8765`) and a test site for per-IP links (`python extension/dev/landing.py 8780`). Terms used in the code and docs are in [CONTEXT.md](CONTEXT.md), decisions in [docs/adr/](docs/adr/).

Data and log live in `%APPDATA%\downloadmanagersquared\` (`settings.json`, `downloads\`, `torrents\`, `engine.log`). Unreadable files are renamed to `….broken`, with a notification.

## Licence

MIT, see [LICENSE](LICENSE). The zip's `THIRD-PARTY-NOTICES` lists the engine's libraries and their licences.
