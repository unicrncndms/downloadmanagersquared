# Store listings

Copy for the Chrome Web Store (CWS) and addons.mozilla.org (AMO). Fill each store's form from the matching section. Nothing here is submitted by itself.

Screenshots (1280x800) and the small promo tile (440x280) are in `docs/img/store/`:

- `01-speed.png`: "Every connection adds speed": three connections busy, the header total is their sum
- `02-map.png`: "See which connection fetched what": one download's map and per-connection speeds
- `03-folder.png`: "Unpacks while it downloads": a Download as folder extracting while it downloads
- `04-connections.png`: "All your connections, side by side": the Connections page
- `promo-small-440x280.png`: CWS small promo tile (the mark and "DMS²" on the accent blue)

Use shots 1 to 4 on both stores. There is no per-IP-links shot: the UI only shows a small "own link" tag for it, which does not read in a screenshot.

Made by `docs/img/store/src/render.ps1`: it starts the UI harness and renders `extension/dev/store/shot.html?s=<shot>` with headless Edge (`--window-size=1280,800 --force-device-scale-factor=1`). That page puts a caption and the real UI page (fixture `store`: made-up downloads, 22 + 12 + 6 = 40 MB/s over Ethernet, Wi-Fi and a phone hotspot) in a scaled, cropped iframe framed as a window, in the UI's own font and palette. It lives under `extension/dev/` because the harness server only serves that folder. Edit the captions and crops in `shot.html`, the data in the `store` fixture in `extension/dev/fixtures.js`, and run the script again after a UI change. The promo tile is separate.

URLs used by both:

- Homepage: https://github.com/unicrncndms/downloadmanagersquared
- Support: https://github.com/unicrncndms/downloadmanagersquared/issues
- Privacy policy: https://github.com/unicrncndms/downloadmanagersquared/blob/main/PRIVACY.md
- Engine: https://github.com/unicrncndms/downloadmanagersquared/releases/latest

---

## Chrome Web Store

**Name:** downloadmanagersquared (DMS²)

**Summary** (104 of 132 characters; also the manifest `description` of the Chromium build):

> Download big files over all your network connections at once. Needs the free Windows engine from GitHub.

**Category:** Tools (under Productivity). It is the closest fit: a download helper, not a planner or a communication tool.

**Language:** English

**Detailed description** (plain text):

```
Needs the free Windows engine from GitHub; does nothing without it. Windows 10 or 11 only.

DMS² downloads one big file over all your network connections at once: Ethernet, Wi-Fi, a tethered phone, a VPN. Every connection adds speed. Using all of them is the point.

How it works
- You install the DMS² engine, a small free Windows app: https://github.com/unicrncndms/downloadmanagersquared/releases/latest
- This extension hands the engine the downloads it should take: files above a size you choose (100 MB by default), .torrent and Metalink files, and magnet: links. You can also right-click a link and choose "Download with DMS²".
- The engine does the downloading. This extension shows its list, with a square map of each file that shows which connection fetched which part, and has the settings page.
- If the engine can't take a download, your browser downloads it as usual.

What you get
- Several connections per file, each with its own speed
- Pause, resume, queue, reorder
- Torrents and magnet links
- Metalinks and mirrors
- Optional VPN tunnels as extra connections
- Free and open source (MIT)

Privacy
No account, no analytics, no telemetry. The extension sends data only to the engine on your own PC, through Chrome's native messaging. The engine contacts the sites you download from, and api.github.com once a day to see if a newer version exists. Details: PRIVACY.md in the project.

Not in the Chrome version yet: sites that give each IP address its own download link. Only the Firefox version handles those for now.

Source code and issues: https://github.com/unicrncndms/downloadmanagersquared
```

**Single purpose:** Hand large downloads, torrents and magnet links from the browser to the DMS² engine on the user's PC, which downloads them over all the user's network connections at once, and show the engine's download list.

**Permission justifications**

- `nativeMessaging`: The only channel to the engine (`dms-host.exe`). Every download, setting and list update goes through it; nothing is sent anywhere else.
- `downloads`: Cancels and erases the browser's own download after the engine has taken it over. Catches downloads (`downloads.onCreated`) and gives one back to the browser when the engine can't take it.
- `webRequest`: Non-blocking. Reads a page request's `Referer` header (`onSendHeaders`) so the engine can fetch the file as the user's browser would. No request is changed or blocked.
- `webNavigation`: Records which pages a tab showed (including history-state changes) to find the page a download started on. The engine needs it for sites that serve the file from a landing page.
- `cookies`: Reads the cookies the browser would send for a download's URL when no request header is available (downloads from the context menu or the browser's own download list), so protected files can be fetched with the user's session. They go to the local engine only.
- `tabs`: Reads the open tabs at startup and a new tab's opener, to keep the page history of each tab. Opens the settings page and the one-time welcome page.
- `storage`: Keeps the page history of recent downloads in `storage.local`, in this browser only.
- `contextMenus`: Adds "Download with DMS²" to the right-click menu of links and pages.
- `notifications`: Tells the user that DMS² took a download, or that it couldn't and the browser downloads it instead.
- Host permission `<all_urls>`: Downloads, magnet links and landing pages can be on any site, and nobody knows which in advance. It is needed to read the headers of any download, to read that site's cookies, and to run the content script that catches `magnet:` link clicks on any page. The extension makes no requests of its own to these sites.

**Remote code:** No. All JavaScript is in the package. No `eval`, no remotely hosted scripts, nothing downloaded and run. (The welcome page's button only opens the engine's zip on GitHub as a normal browser download; the extension never runs it.)

**Data usage** (the "Privacy practices" tab)

Data types to tick:

- Web history: yes. The pages that led to a download are kept and sent to the local engine.
- Website content: yes. The download's URL, file name, cookies and referer, sent to the local engine.
- Authentication information: yes, for the same reason: the cookies the browser sent with a download go to the local engine, so it can fetch the file logged in.
- Everything else (personally identifiable information, health, financial, location, user activity, personal communications): no.

Certifications, all to be ticked:

- I do not sell or transfer user data to third parties, outside of the approved use cases.
- I do not use or transfer user data for purposes that are unrelated to my item's single purpose.
- I do not use or transfer user data to determine creditworthiness or for lending purposes.

Why the data is handled: only to carry out the user-facing feature. It goes to the user's own engine on the same PC, never to a server of the developer (there is none).

**Privacy policy URL:** https://github.com/unicrncndms/downloadmanagersquared/blob/main/PRIVACY.md

**Homepage URL:** https://github.com/unicrncndms/downloadmanagersquared

**Support URL:** https://github.com/unicrncndms/downloadmanagersquared/issues

**Test instructions** (if the form asks): The extension needs the Windows engine from the releases page (`downloadmanagersquared-windows.zip`; run `dms-engine.exe` on Windows 10 or 11). Without it the popup says "Engine not installed". With it: lower the size threshold in Settings, Downloads, and download any file. The engine takes it over and the popup shows it running.

---

## AMO (addons.mozilla.org)

**Name:** downloadmanagersquared (DMS²)

**Summary** (under 250 characters; this one is 178):

> Needs the free Windows engine from GitHub; does nothing without it. Downloads big files over all your network connections at once, including sites that give each IP its own link.

**Description:**

```
**Needs the free Windows engine from GitHub; does nothing without it.** Windows 10 or 11 only. Get it here: https://github.com/unicrncndms/downloadmanagersquared/releases/latest

DMS² downloads one big file over all your network connections at once: Ethernet, Wi-Fi, a tethered phone, a VPN. Every connection adds speed. Using all of them is the point.

**How it works**
- You install the DMS² engine, a small free Windows app. It does the downloading.
- This add-on hands the engine the downloads it should take: files above a size you choose (100 MB by default), .torrent and Metalink files, and magnet: links. You can also right-click a link and choose "Download with DMS²".
- The add-on shows the engine's list, with a square map of each file that shows which connection fetched which part, and has the settings page.
- If the engine can't take a download, Firefox downloads it as usual.

**Sites that give each IP its own link**
Some sites tie a download link to the IP address that asked for it. For each connection, DMS² opens the page in a temporary container that reaches the internet only through that connection, replays the clicks you showed it once, and hands the link to the engine. With no recorded clicks yet, it asks you to click through once.

**What you get**
- Several connections per file, each with its own speed
- Pause, resume, queue, reorder
- Torrents and magnet links, Metalinks and mirrors
- Optional VPN tunnels as extra connections
- Free and open source (MIT)

**Privacy**
No account, no analytics, no telemetry. The add-on sends data only to the engine on your own PC, through Firefox's native messaging. The engine contacts the sites you download from, and api.github.com once a day to see if a newer version exists.
```

**Categories:** Download Management. One is enough; do not add a second just to fill the slot.

**Tags:** download manager, downloads, multiple connections, torrent, magnet, metalink, bandwidth

**Homepage:** https://github.com/unicrncndms/downloadmanagersquared

**Support site:** https://github.com/unicrncndms/downloadmanagersquared/issues

**Support email:** leave empty (the issue tracker is the support route), unless AMO insists.

**License:** MIT License (pick it from the list; no custom text).

**Privacy policy:** https://github.com/unicrncndms/downloadmanagersquared/blob/main/PRIVACY.md

**This add-on requires external software:** Yes. The free DMS² engine for Windows (`dms-engine.exe`, with `dms-host.exe`), from https://github.com/unicrncndms/downloadmanagersquared/releases/latest . The add-on's welcome page explains it and has a button that opens the zip's URL for a normal browser download; the add-on never runs it.

**Notes to reviewer:** Paste the text of `docs/amo-reviewer-notes.md` (what the add-on does, permission table, what goes to the native app, how to try it). Upload the source only if AMO asks: the add-on is plain JS with no build step, the `extension/` folder of the repository; `build.ps1` only copies it and writes the version.

**Data collection:** already declared in `manifest.json` (`browsingActivity`, `websiteActivity`, `websiteContent`, all required; the data goes only to the local engine).

**Screenshots:** the four PNGs `01` to `04` in `docs/img/store/`.
