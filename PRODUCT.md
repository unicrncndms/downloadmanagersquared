# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Two audiences of similar weight, confirmed by the user:

- **Speed seekers with more than one connection.** A home line plus a phone hotspot or a second line, who want one file to use both at once.
- **Power downloaders on hard hosts.** People who fetch from file hosts that tie each link to an IP address, and who need the engine to open the site once per connection and repeat the clicks.

Both use Windows 10/11 with Firefox, LibreWolf, Waterfox or Floorp. They are comfortable with a download manager and expect it to stay out of the way.

## Product Purpose

DMS² (downloadmanagersquared) downloads one file over every network connection at once, so their speeds add up. It runs in the Windows tray as an engine, and its pages (the browser popup, the Downloads window, and Connections & settings) show and control the work.

Success means a file arrives faster than on any single connection, with nothing downloaded twice, and the download survives crashes, sleep and restarts. It also means the user can see what each connection is doing without decoding internals.

## Positioning

A download manager that adds up connections per file. Others open many requests over one connection. DMS² fetches the pieces of one file over every connection at the same time, and runs its own per-connection WireGuard tunnels without a driver and without admin rights.

It is free and open source (MIT), with no account, no server and no telemetry. The privacy promise is part of the product and must stay visible in its behavior.

## Operating Context

- Used alongside a browser: the extension takes over qualifying downloads (files over 100 MB, magnet links, `.torrent`, Metalink) and offers *Download with DMS²* on link menus.
- The engine runs in the tray; the Downloads window and Connections & settings open from it. The tray menu and setup window stay native Windows UI.
- Users often arrive with a link in hand and a plan they already made in the browser, so the first moment matters less than the steady state of a long queue.
- Install is a zip with a SmartScreen warning (the program is not code-signed). The UI must not add to that anxiety.

## Capabilities and Constraints

- Platform: Windows 10/11 only. Browsers: Firefox, LibreWolf, Waterfox, Floorp (Chromium support is an open spec, not shipped).
- Pages run in two hosts: the Firefox extension popup and the engine's WebView2 Downloads window (`extension/ui/host.js`). One set of pages covers both.
- Core features that the UI must show: parallel connections per interface, backoff ("Server busy"), mirrors, checksums, a drag-and-arrow queue, *Download as folder* for `.zip`, `.7z`, `.rar` and split archives, the password for archives, IPv4 and experimental IPv6, VPN tunnels per connection, port forwarding, torrents.
- Shared vocabulary lives in `CONTEXT.md`: use its terms (interface, connection, chunk, link, probe, mirror, backoff, landing page, failed link) in UI wording. Avoid its listed alternatives in user-facing text.
- A download cannot go faster than the server sends, and Wi-Fi and Ethernet to one router share one line. The UI must not promise speed the product cannot deliver.
- UI language: English only for now. Layout should still tolerate longer strings if translation is added later.

## Brand Commitments

- Name: **DMS²** in prose, `downloadmanagersquared` as the package and repository name. Keep the superscript two.
- Voice: plain and direct, as in the README. Short sentences; say what happens and what the user must do.
- Icons: the extension icons live in `extension/icons/`. No other logo or mark is recorded.

## Evidence on Hand

- README.md, PRIVACY.md, SECURITY.md and CONTEXT.md describe the behavior and the vocabulary.
- `.scratch/design-overhaul/spec.md` records the decisions so far: the web pages only are in scope for the visual overhaul; the accent follows Firefox's theme by default, with presets and a custom picker; the tray menu and setup window stay native.
- No screenshots, customer quotes, reviews, benchmarks, press or case studies are on file. Do not fabricate any of them. Speed claims must match the README wording ("close to the sum" only when the connections are truly separate lines).

## Product Principles

- **Make the sum visible.** Show each connection's share of the work and its state, so the added speed is believable.
- **Keep the privacy promise in the interface.** No account, no telemetry, nothing sent to the developer; say where data stays when it matters.
- **Be honest about limits.** Say when a connection is backing off or a server is the bottleneck, not that the app is slow.
- **Stay out of the way in a long queue.** Density and scanability beat decoration; the user returns to check progress, not to admire the screen.
- **Native where the OS owns the moment.** Tray and setup stay Windows-native; the web pages carry the product's look.

## Accessibility & Inclusion

- WCAG 2.2 AA for the web pages (popup, Downloads window, Connections & settings): text and interface contrast, full keyboard operation, visible focus, and no information carried by color alone. Dark theme must meet the same standard.
