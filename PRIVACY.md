# Privacy

DMS² has no server of its own and collects nothing. **It talks only to the sites and torrent swarms you download from, and to the few services listed under *Who else DMS² contacts* below**, through your own connections (and through your VPN, if you set one up). There is no telemetry and no crash reporting. The engine asks GitHub once a day whether a newer release exists; it downloads nothing. The developer receives no data of any kind, and nothing is sold or shared. Firefox updates the extension from addons.mozilla.org, or from the project's GitHub releases if you installed it from there.

## What the extension tells the engine

The Firefox extension talks only to the DMS² engine on the same PC (through Firefox's native messaging and a named pipe that only your Windows user can open). For each download it takes over, it sends:

- the download's URL, file name and size, and the pages that led to it (the page you started it on and the ones before, to find the landing page);
- the Referer and your browser's User-Agent, so the site sees the same browser;
- the cookies your browser sent for that download (or, failing that, the cookies it would send for that URL in that container). They go to the local engine only. The engine keeps them in your Windows profile, encrypted with Windows so only your user can read them, until the download completes or you remove it, and sends them only to that site and its subdomains.

The extension also keeps, in Firefox's own storage, the clicks it learned per site (button texts and positions, and how long each took), and which pages led to recent downloads.

## Pages DMS² opens

Pages DMS² opens to fetch a link run in a temporary container that only reaches the internet through the chosen connection, and is removed with its cookies when the link is fetched. It starts with a copy of the cookies your browser would send to that page, so you are logged in there too (never from a private window). That site then sees your session from that connection's IP address as well; a site that ties a login to one address may log you out. If the engine isn't running, those requests are blocked.

## What the engine keeps

In `%APPDATA%\downloadmanagersquared\`: your settings, the download list (with the URLs, landing pages and, until each download completes, its cookies, encrypted with Windows so only your user can read them; cookies an older version saved readable are encrypted as soon as the engine starts, and a download file that can't be read is set aside as `….broken` without its cookies), torrents, the log (no URLs), imported WireGuard configs, and a PIA login if you use one, encrypted with Windows so only your user can read it. Uninstalling can delete all of it; your downloaded files are always kept.

## Other users of the PC

The engine accepts requests only from processes of your Windows user, and its proxies connect only to public internet addresses. Programs running as your own user, and administrators of the PC, are not kept out.

## Who else DMS² contacts

Each of these sees the public IP address of the connection used, as any site you visit does.

- **Torrent swarms.** Torrents upload as well as download: while a torrent downloads, and while it seeds afterwards, DMS² sends pieces of it to other peers. Peers, trackers and the DHT see your IP address and which torrent you share. For private torrents DMS² stays off the DHT and peer exchange.
- **Your router.** For torrents, DMS² may ask your router (UPnP or NAT-PMP) to forward a port to your PC.
- **Private Internet Access**, only if you set it up: your login, the server list and the tunnels go to PIA.
- **Public DNS resolvers**, only for a connection that has no DNS server of its own: Cloudflare (1.1.1.1) and Google (8.8.8.8), or over IPv6 2606:4700:4700::1111 and 2001:4860:4860::8888. They see the names that connection looks up.
- **stun.cloudflare.com**, only when you use *Check lines*: one request per connection, to learn its public IP address and find connections that share one.
- **GitHub**, once a day: the engine asks `api.github.com` for the latest release's version, to show *Update available*. The request carries no data about you or your downloads. GitHub is also contacted when you click *Add to …* in setup, or open the releases page: the extension is downloaded from the project's GitHub releases.

## Contact

Questions about privacy: open an issue at https://github.com/unicrncndms/downloadmanagersquared/issues. Security problems: see [SECURITY.md](SECURITY.md).
