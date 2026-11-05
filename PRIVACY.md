# Privacy

DMS² has no server of its own and collects nothing. **Nothing leaves your PC except the downloads themselves**: the requests to the sites and torrent swarms you download from, through your own connections (and through your VPN, if you set one up). There is no telemetry, no crash reporting and no update check by the engine. The extension is updated by Firefox from the project's GitHub releases.

## What the extension tells the engine

The Firefox extension talks only to the DMS² engine on the same PC (through Firefox's native messaging and a named pipe that only your Windows user can open). For each download it takes over, it sends:

- the download's URL, file name and size, and the pages that led to it (the page you started it on and the ones before, to find the landing page);
- the Referer and your browser's User-Agent, so the site sees the same browser;
- the cookies your browser sent for that download (or, failing that, the cookies it would send for that URL in that container). They go to the local engine only. The engine keeps them in your Windows profile, encrypted with Windows so only your user can read them, until the download completes or you remove it, and sends them only to that site and its subdomains.

The extension also keeps, in Firefox's own storage, the clicks it learned per site (button texts and positions, and how long each took), and which pages led to recent downloads.

## Pages DMS² opens

Pages DMS² opens to fetch a link run in a temporary container that only reaches the internet through the chosen connection, and is removed with its cookies when the link is fetched. If the engine isn't running, those requests are blocked.

## What the engine keeps

In `%APPDATA%\downloadmanagersquared\`: your settings, the download list (with the URLs, landing pages and, until each download completes, its cookies, encrypted with Windows so only your user can read them; cookies an older version saved readable are encrypted as soon as the engine starts, and a download file that can't be read is set aside as `….broken` without its cookies), torrents, the log (no URLs), imported WireGuard configs, and a PIA login if you use one, encrypted with Windows so only your user can read it. Uninstalling can delete all of it; your downloaded files are always kept.

## Other users of the PC

The engine accepts requests only from processes of your Windows user, and its proxies connect only to public internet addresses. Programs running as your own user, and administrators of the PC, are not kept out.
