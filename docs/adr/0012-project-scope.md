# What DMS² is, and what it is not

Date: 2026-11-22
Status: accepted

## Context

Outside reviews and feature requests keep asking for the same things: other platforms, categories, a full torrent client, more VPN providers. Each needs a reasoned answer, and the answer should be the same every time. This records the scope so a request can be answered by pointing here.

## Decision

**DMS² is** a Windows download manager that downloads one file over every network connection at once, so their speeds add up. Its pieces:

- The engine in the tray, its Downloads window and Connections & settings, and the browser extension that takes over downloads.
- HTTP(S), FTP/FTPS, SFTP, Metalink mirrors and torrents, each spread over the connections.
- Resolving links from hosts that tie a link to one IP address: the site is opened once per connection and the clicks are repeated.
- WireGuard tunnels per connection, without a driver or admin rights: a plain WireGuard config, and PIA.
- *Download as folder* for archives, extracted while they download.
- Making downloads dependable and failures understandable: recovery after crashes, sleep and full drives, plain status text, diagnostics.
- Measured speed, published: `docs/benchmarks/` and the README may show DMS² against *a conventional download manager*, the average of two or three common ones (for example aria2, IDM and JDownloader), measured the same way. The raw rows in `results.csv` name each tool; what the README and summaries show is the average.
- A few behaviour options, each chosen because no default fits everyone: folder rules by extension or site, sites always or never taken over, what to do when the file already exists, renaming a download.

**DMS² is not**, and will not take requests for:

- **Other platforms** (Linux, macOS, mobile). WebView2, `winutil` and the Windows networking code make it close to a rewrite.
- **A category system.** Folder rules only; no category views, no import/export, no configurable columns, no per-download schedules or per-download bandwidth limits.
- **A full torrent client.** No DHT or tracker settings, no seeding schedules, no sequential download. Picking a torrent's files and a seed ratio stay.
- **More VPN providers** beyond a WireGuard config and PIA.
- **Other link handlers**: WebDAV, cloud-storage links.
- **Replaying POST-created links or real clicks.** Listed in the README's limitations.
- **A public roadmap.**
- **Comparisons against one named product.** No summary, chart or claim sets DMS² against a single named download manager.

## Consequences

- A request on the *not* list is answered with a link to this file. Changing the list is a new ADR that replaces this one.
- The README's Limitations section links here.
- A new behaviour option has to show that no good default makes it unnecessary.
