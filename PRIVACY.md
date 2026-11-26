# Privacy

DMS² has no server of its own and collects nothing. **It talks only to the sites and torrent swarms you download from, and to the few services listed under *Who else DMS² contacts* below**, through your own connections (and through your VPN, if you set one up). There is no telemetry and no crash reporting. The engine asks GitHub once a day whether a newer release exists; it downloads nothing unless you press *Update now* (see below). The developer receives no data of any kind, and nothing is sold or shared. Your browser updates the extension from its store (addons.mozilla.org or the Chrome Web Store), or from the project's GitHub releases if you installed it from there.

## What the extension tells the engine

The browser extension (Firefox, or Chrome and other Chromium browsers) talks only to the DMS² engine on the same PC (through the browser's native messaging and a named pipe that only your Windows user can open). For each download it takes over, it sends:

- the download's URL, file name and size, and the pages that led to it (the page you started it on and the ones before, to find the landing page);
- the Referer and your browser's User-Agent, so the site sees the same browser;
- the cookies your browser sent for that download (or, failing that, the cookies it would send for that URL in that container). They go to the local engine only. The engine keeps them in your Windows profile, encrypted with Windows so only your user can read them, until the download completes or you remove it, and sends them only to that site and its subdomains.

The extension also keeps, in the browser's own storage, a copy of the sites DMS² learned (see *What the engine keeps*) and which pages led to recent downloads. In Chrome and Edge it keeps the pages each open tab showed in the browser's session storage, which the browser clears when it closes, so it still knows them after Chrome pauses the extension in the background.

## Pages DMS² opens

In Firefox, pages DMS² opens to fetch a link run in a temporary container that only reaches the internet through the chosen connection, and is removed with its cookies when the link is fetched. It starts with a copy of the cookies your browser would send to that page, so you are logged in there too (never from a private window). That site then sees your session from that connection's IP address as well; a site that ties a login to one address may log you out. If the engine isn't running, those requests are blocked.

For downloads from Chrome and other Chromium browsers (and from any browser while no Firefox extension is connected), the engine opens those pages itself, in Chrome or Edge, which it starts with a profile of its own (`resolver-profile` in the data folder), never your browser's. Each connection gets a private browsing context that reaches the internet only through that connection's proxy and keeps nothing on disk; it is closed, with its cookies, when the last page for that connection is done, and that browser closes a few minutes after the last one. It starts with a copy of the cookies your browser would send to that page, handed over by the extension as the page opens (never from a private window), so you are logged in there too; if the extension doesn't answer, with the cookies your browser sent with the download, for that same site only.

## What the engine keeps

In `%APPDATA%\downloadmanagersquared\`: your settings, the download list (with the URLs, landing pages and, until each download completes, its cookies, encrypted with Windows so only your user can read them; cookies an older version saved readable are encrypted as soon as the engine starts, and a download file that can't be read is set aside as `….broken` without its cookies), torrents, the log (see below), imported WireGuard configs (encrypted with Windows, as their private keys are in them), the login of an `ftp://user:password@…` link (encrypted with Windows, only until that download completes; the link itself is saved without it), the SFTP logins you gave (a password, or a key file's path and passphrase; encrypted with Windows, kept per host and user until you remove them in Settings) and the SSH host keys you trusted (public keys), the sites DMS² learned, one list for every browser (`recipes.json`: each landing page's host, the clicks there as button texts and positions with how long each took, and the browser it was learned in; `stepbacks.json`: how many pages back each site's landing page is), the password you typed for an encrypted archive (encrypted with Windows, and only until that archive is extracted), and a PIA login if you use one, encrypted with Windows so only your user can read it. Uninstalling can delete all of it; your downloaded files are always kept.

**The log** (`engine.log`, and `crash.log` if the engine crashes) stays on your PC: DMS² never sends it anywhere. It leaves only when you attach it to an issue, or paste what *Copy diagnostics* puts on your clipboard. What both hold is up to *Settings → About → Log privacy*: *Standard*, the default, leaves out passwords, cookies and tokens, links' paths and queries, file and folder names in paths, and your public IP address, and keeps the sites you download from (a file's name written without its folder, as some errors do, can still show); *Increased* leaves out the sites and every file name too; *Off* writes everything as it happens, links included. Changing it doesn't rewrite lines already in the log. `crash.log` holds only the program's own crash traces and isn't filtered.

## Other users of the PC

The engine accepts requests only from processes of your Windows user, and its proxies connect only to public internet addresses. Programs running as your own user, and administrators of the PC, are not kept out.

## Who else DMS² contacts

Each of these sees the public IP address of the connection used, as any site you visit does.

- **Torrent swarms.** Torrents upload as well as download: while a torrent downloads, and while it seeds afterwards, DMS² sends pieces of it to other peers. Peers, trackers and the DHT see your IP address and which torrent you share. For private torrents DMS² stays off the DHT and peer exchange.
- **Your router.** For torrents, DMS² may ask your router (UPnP or NAT-PMP) to forward a port to your PC.
- **Private Internet Access**, only if you set it up: your login, the server list and the tunnels go to PIA.
- **Public DNS resolvers**, only for a connection that has no DNS server of its own: Cloudflare (1.1.1.1) and Google (8.8.8.8), or over IPv6 2606:4700:4700::1111 and 2001:4860:4860::8888. They see the names that connection looks up.
- **stun.cloudflare.com**, only when you use *Test if they are separate lines*: one request per connection, to learn its public IP address and find connections that share one.
- **GitHub**, once a day: the engine asks `api.github.com` for the latest release's version, to show *Update available*. The request carries no data about you or your downloads. GitHub is also contacted when you click *Add to …* in setup, or open the releases page: the extension is downloaded from the project's GitHub releases. When you press *Update now*, the engine also downloads the release zip from github.com and checks its SHA-256.

## Contact

Questions about privacy: open an issue at https://github.com/unicrncndms/downloadmanagersquared/issues. Security problems: see [SECURITY.md](SECURITY.md).

## Extension permissions

The Firefox extension asks for these permissions. The Chrome build asks for the same, except `proxy`, `contextualIdentities`, `webRequestBlocking`, `menus` and `theme`, which the Chrome build leaves out; `<all_urls>` is a host permission there. It does not open temporary containers: the engine opens those pages itself (see *Pages DMS² opens*).

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
| `storage` | Keeping a copy of the learned sites, and the containers, in this browser |
| `contextMenus`, `menus` | The *Download with DMS²* entries on links and pages, hidden where they don't apply |
| `theme` | Reading your browser theme's accent colour, to colour the pages |
| `notifications` | Telling you when a download is taken over or can't be, and when a site needs your clicks or a captcha |
| `<all_urls>` | Seeing downloads, magnet and ftp:// links and landing pages on any site |
