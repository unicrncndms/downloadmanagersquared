# Using DMS²

DMS² splits each download across every internet connection your PC has (Ethernet, Wi-Fi, a phone hotspot, a USB tether, VPN tunnels) and adds their speeds up. It is two parts: the **engine**, a Windows program that does the downloading and runs in the tray, and a **browser extension** (Firefox and its relatives, Chrome, Edge) that hands it your downloads. Install both as the [README](../README.md#install) says.

- [Adding downloads](#adding-downloads)
- [The download list](#the-download-list)
- [Connections](#connections)
- [Sites with a link per IP](#sites-with-a-link-per-ip)
- [Download as folder](#download-as-folder)
- [Torrents](#torrents)
- [Mirrors and Metalink](#mirrors-and-metalink)
- [FTP and FTPS](#ftp-and-ftps) · [SFTP](#sftp) · [Checksums](#checksums)
- [Settings](#settings) · [VPN](#vpn)
- [Several browsers](#several-browsers)
- [Upgrade and uninstall](#upgrade-and-uninstall)
- [When something goes wrong](#when-something-goes-wrong)

## Adding downloads

- **As usual.** Download a file in the browser: files of 100 MB or more (Settings → Browser downloads) go to DMS² by themselves, and so do magnet links, `.torrent` and Metalink (`.meta4`) files. Smaller files stay in the browser. Sites under *Never take over downloads from* always stay in the browser, and downloads from sites under *Always take over downloads from* go to DMS² whatever their size.
- **The right-click menu.** *Download with DMS²* on a link sends it whatever its size. *Download this page's file with DMS²* on a page sends the page's own address, for a page that is the download.
- **Paste.** Copy one or more links (http, https, ftp, ftps, sftp, magnet) and press **Ctrl+V** anywhere in the list, or use *Add link from clipboard* in the tray icon's menu. Links already in the list are skipped.
- **The Add box.** **+ Add** in the list takes links, a save folder, *Download as folder* for archives, and Options (a checksum, an SFTP key file). *Or add a .torrent or Metalink file from this PC…* there opens Windows' file picker.

## The download list

The extension's toolbar icon shows the list. A left click on the tray icon opens the same list in its own **Downloads window**, with **Settings** as a second tab; the toolbar popup opens the settings from the gear at the top right.

- Each download shows a row of squares, the file's parts, each coloured by the connection that fetched it. Its speed per connection and its time left are beside it.
- **Details** has the rest: the map of the whole file, each connection's requests and speed, the source and landing page, checksums, mirrors, archive extraction and the log.
- The **…** menu: *Start now*, *Move to top*, *Move to bottom*, *Record the site's clicks…* (below), *Download as a file instead* (for Download as folder), *Remove…*.
- **The queue.** *Downloads at once* (Settings, 1 by default) run, the rest wait in order. Drag a download to move it, or drop a waiting one on a running one to take its place.
- Unfinished files end in `.dms.part`. Progress is saved every few seconds, so a download survives a crash, sleep or reboot and carries on.
- *Pause all* and *Resume all* are in the list and in the tray menu. *Quit* in the tray menu stops all downloads.

## Connections

**Settings → Connections** lists every connection Windows has. Each one you switch on downloads its share: a faster connection takes over more of the work, and nothing is fetched twice.

- **Main connection:** used alone for servers that can't split a file. *Automatic* follows Windows' choice.
- **Options** per connection: its name, parallel requests (4), a download and an upload limit, and whether it seeds torrents. Click a connection's tile to pick its colour.
- **Hide** folds a connection you never use away under *Hidden connections*, where *Unhide* brings it back.
- VPN adapters (a VPN program running in Windows) start off; switch one on to use it as an extra connection.
- **Test if they are separate lines** checks which connections reach the internet through the same line, by asking each for its public IP address (stun.cloudflare.com). Two that share a line don't add speed.
- **Enable IPv6 (experimental)** (off) uses IPv6 too; each connection shows a badge with what it has.

### Keep Wi-Fi connected next to a cable

Windows is set by default to *minimize the number of simultaneous connections*: while a cable or a phone tethered over USB is connected, it may drop Wi-Fi, and DMS² loses that connection. When this can happen, Connections shows a note with **Keep all connected**. It asks for administrator rights and sets Windows to keep every connection; your connections drop for a few seconds while Windows applies it.

By hand, in PowerShell as administrator (this works on Windows Home too, which has no Group Policy editor):

```powershell
New-ItemProperty 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\WcmSvc\GroupPolicy' -Name fMinimizeConnections -PropertyType DWord -Value 0 -Force; Restart-Service WcmSvc -Force
```

On Pro, the same setting is in the Group Policy editor: *Computer Configuration → Administrative Templates → Network → Windows Connection Manager → Minimize the number of simultaneous connections to the Internet or a Windows Domain*, set to *Enabled* with *0 = Allow simultaneous connections*. To undo it, delete the `fMinimizeConnections` value (or set the policy back to *Not configured*) and restart Windows.

## Sites with a link per IP

Some file hosts hand out download links that only work from the IP address that asked for them. For those, DMS² opens the site once per connection, through that connection only, and takes each connection's own link. Your browser's own download keeps its link and goes on the main connection.

**The first time:** a window opens with a bar at the top, "DMS² is watching". Click through the site until the download starts. DMS² remembers those clicks (the site's *recipe*) and repeats them by itself from then on.

- **Start on the file's own page**, not on a list of files: the clicks are repeated for every download from that site. If the window opened on the wrong page, **◀ Back** and **Forward ▶** go through the pages you visited on the way to this download.
- **?** explains the bar. **Skip this connection** closes the window; that connection's download fails, the others go on.
- In Firefox these pages open in a temporary container; for Chrome and Edge, DMS² opens them in a Chrome or Edge window of its own, never your browser's profile. Either way they start with a copy of your cookies for that site, so you are logged in there too (never from a private window).

**Later downloads** from the site replay the clicks out of sight, a few seconds per connection, while the site's countdowns run as usual. You see nothing unless something needs you:

- **A captcha** that doesn't pass by itself within about 12 seconds brings its window up with a notification. Solve it, and the window goes back out of sight and carries on.
- **A click that no longer works** (the site changed) brings the window up with the record bar from that step: click through once more, and the recipe is updated.

**Learned sites** are listed under **Settings → Sites and logins**, with where each was learned. DMS² keeps one list for every browser: a site learned in Firefox is used for Chrome downloads too, and the other way round. *Forget* removes one at once (*Undo* in the message puts it back).

**Record the site's clicks** (a download's … menu, or Details) records a site's recipe ahead of time, while you are there, so a link that expires later can be renewed without you.

Replayed clicks are synthetic: a site that only accepts real clicks asks every time. Many file hosts limit free downloads per IP address, and their terms may not allow getting around that. Following each host's terms is up to you.

## Download as folder

Switch it on with the box when adding an archive link, with the switch on a download's card, or for every new archive in the settings.

- It asks where to extract, starting at the save folder; the download waits under *Needs you* until you answer.
- DMS² fetches the archive's list of files first, then downloads front to back and extracts each file as soon as its bytes are there. A second bar on the card shows the extraction, what it waits for, and the time left.
- Every file is checked against the archive's own checksum. One that comes out wrong is downloaded again.
- You can change the folder it extracts into while it runs, on the same drive or another.
- **Split archives:** add all the parts. They show as one card with one switch, ask where to extract once, on part 1 (the other parts follow that answer), download in the order that keeps the least on disk, and extract as one archive. A part that isn't added stops extraction there: "Part 2 missing". *Download a split archive's parts at once* in the settings trades disk space for speed.
- **Passwords:** an encrypted 7z or RAR asks for its password on the card and keeps downloading meanwhile. An encrypted ZIP is downloaded as a normal file.
- It needs a server that allows split downloads. A ZIP must be compressed with Stored, Deflate, Deflate64 or Zstandard. Anything it can't extract is downloaded as a normal file, with the reason in Details.
- **Keeping the archive:** by default (*Automatic*) the archive is kept until it is extracted when the drive has room for it, so an update, reboot or crash while extracting downloads nothing again; on a nearly full drive its space is freed as it extracts. Details can set it per download: *Free while extracting*, *Keep until extracted*, or *Keep the archive* to have the archive file too ([ADR 0007](adr/0007-keeping-the-archive-for-interruption-recovery.md)).
- There is no whole archive to check at the end, so a checksum you entered turns the mode off.

## Torrents

Magnet links and `.torrent` files download over all your connections too.

- A torrent with several files shows *Choose files* first; untick the ones you don't want.
- When it finishes, it seeds (uploads to other peers) until it reaches the seed ratio (Settings → Torrents, 1 by default), then stops.
- **Your IP address:** peers and trackers see the IP address of every connection a torrent uses. With the VPN set to Off, DMS² asks before such a torrent starts. With *VPN only*, they see only the tunnels' addresses.
- For incoming peers, DMS² asks your router to forward its port (UPnP or NAT-PMP), or the VPN provider for one; each connection shows whether peers can reach it.

## Mirrors and Metalink

A Metalink file, or a server that names mirrors with a checksum, lets each connection fetch from its own mirror, so one slow mirror doesn't hold back the rest. A mirror serving a different file is dropped. Details lists the mirrors and which connection uses which.

## FTP and FTPS

Paste an `ftp://` or `ftps://` link into **Add download**; a link clicked on a page is taken too, as Firefox no longer opens them. The server's `SIZE` and `MDTM` give the size and version, and each connection logs in on its own and fetches a part with `REST` and `RETR`, over the connections you switched on (and the VPN, as set). Login is anonymous unless the link has one (`ftp://name:password@host/file`): the password is kept encrypted with Windows and is not shown or logged. A server that allows only one login at a time, or does not support `REST`, is downloaded over one connection. `ftp://` sends the password and the file unencrypted; `ftps://` is encrypted, with a certificate that must be trusted (port 990 is implicit TLS, any other port asks for it with `AUTH TLS`). The data connection always goes to the address the server was reached at, whatever it names in its passive reply.

## SFTP

Paste an `sftp://user@host/path` link into **Add download**, with the password in it (`sftp://user:password@host/path`) or, under Options, the path of a private key file and its passphrase. Each connection opens an SSH session of its own and reads its part of the file, over the connections you switched on (and the VPN, as set); the file's size and modification time are its version. The login is kept encrypted with Windows per host and user, so a later link `sftp://user@host/other` needs none; **Settings > SFTP logins** lists them (never the secret) and removes them. The first time a server is met, its row asks whether to trust the key's SHA256 fingerprint (**Trust**, or remove the download): nothing, not even the login, is sent before that. A server whose key later differs fails the link with a warning and is never accepted by itself: if you are sure the key really changed, **Settings > SFTP logins** lists the trusted keys and **Forget** removes one, so the next link asks again. A server that refuses more sessions or connections costs a connection, not the link.

## Checksums

A download's **Details → Checks → Edit** takes a SHA-256 or MD5. DMS² checks the file when it finishes (or right away if it already has), and a download that doesn't match is kept as `.dms.part` instead of finishing. Files from Google Cloud Storage (and sites served from it) carry their MD5, which is checked the same way when you haven't entered one.

## Settings

The sections are in the side bar; *Find a setting* searches all of them.

- **Connections:** see [Connections](#connections): the main connection, default requests per connection, and IPv6 (experimental, off). VPN adapters start off.
- **VPN:** see below.
- **Downloads:** save folder, folders by file type or site, and what to do if the file already exists.
- **Archives:** Download as folder for new archives (off), keep the archive (Automatic), a split archive's parts at once (off).
- **Browser downloads:** takeover size (100 MB), sites never or always taken over.
- **Queue and speed:** downloads at once (1); the queue window: optionally, per weekday, the hours the queue runs (an end before the start runs past midnight). Outside it nothing starts, running downloads pause and seeding torrents stop; they resume when it begins. A download you start or resume yourself still runs. No day set: no limit. Then a download and an upload limit for all connections together (none; each connection can have its own in its Options), the unit speeds are shown in (sizes and speeds use base 1000: 1 MB = 1,000,000 bytes), and the time-left averaging.
- **Torrents:** seed ratio (1), port for incoming peers, peers per torrent for each connection (100).
- **Sites and logins:** the learned sites, one list for every browser, with where each was learned (*Forget* makes DMS² ask again), and the SFTP logins and trusted server keys, removable.
- **General:** start with Windows, notifications, keep the PC awake; theme, accent colour, and how much the background is tinted with the accent (off).
- **Keyboard:** the shortcuts of the downloads list.
- **About:** the versions, *Check for updates*, the release notes, *Log privacy* (see [When something goes wrong](#when-something-goes-wrong)), and restoring the default settings.

### VPN

Each connection can get its own WireGuard tunnel, for downloads, torrents or both. Choose **Off** (the connections without a tunnel only), **VPN and direct** (the tunnels too) or **VPN only** (the tunnels only). The tunnels run inside DMS²: no driver is installed and your other programs aren't affected.

- **Private Internet Access:** sign in, and every connection without its own `.conf` gets a tunnel. Under **Tunnels**, each can have a region of its own, or *Spread automatically*: the nearest region no other tunnel is in, those with port forwarding first. Click a tunnel's tile to pick its colour.
- **Other providers:** import a `.conf` per connection (it is kept encrypted with Windows). It must be plain WireGuard with an IPv4 address and an IPv4 DNS server (`DNS =`; IPv6 ones aren't used), one config per connection (a separate device or key at your provider). Obfuscated AmneziaWG configs don't work. If the provider forwards ports by NAT-PMP (Proton VPN on P2P servers, with NAT-PMP on when you make the config), DMS² gets a port for torrents by itself (not yet tested with Proton's servers). For a fixed port from your provider's panel (AirVPN), type it in the **Forwarded port** box next to the `.conf`.
- For sites with a link per IP, use a different server for each connection, or they may share one address.
- Which providers offer `.conf` files, and why PIA works differently: [vpn-providers.md](vpn-providers.md).

## Several browsers

The extension works in Firefox and its relatives (LibreWolf, Waterfox, Floorp), and in Chrome and Edge. Install it in as many as you use: each connects to the same engine. Settings, the download list and the learned sites live in DMS² itself, so every browser shares them; a site learned in Firefox is used for Chrome downloads as well, and the other way round.

## Upgrade and uninstall

**Upgrade:** when a newer release is out, the list says so ("DMS² v… is available") and Settings → About has *Check for updates*; *Update now* downloads it, checks it and installs it, keeping everything. Or run `dms-engine.exe` from a newer zip. The extension updates itself from its store (addons.mozilla.org or the Chrome Web Store); in Firefox you can set **Allow automatic updates** to **Off** for it in `about:addons`.

**Uninstall:** *Settings → Apps → Installed apps → downloadmanagersquared*. Downloaded files are always kept; you choose whether settings and the download list go. Then remove the extension in your browser.

## When something goes wrong

- **"Engine offline" or "Engine not installed"** in the popup: start DMS² from the Start menu (the popup's *Start DMS²* tries it), or install it from the [latest release](https://github.com/unicrncndms/downloadmanagersquared/releases/latest).
- **A download isn't taken over:** it is smaller than the takeover size, its site is under *Never take over downloads from*, or the engine isn't running. Use *Download with DMS²* on its link.
- **"Needs the page"** on a download: its link stopped working and DMS² doesn't know the page it came from. Click it and paste the page's address.
- **A site asks you to click through every time:** it only accepts real clicks, or its buttons change from visit to visit. *Forget* it in Sites and logins and record it again, starting on the file's own page.
- **Only one connection is used:** the server can't split a file (it is downloaded over the main connection), or the other connections are switched off or share one line (*Test if they are separate lines*), or Windows dropped Wi-Fi for a cable ([Keep Wi-Fi connected next to a cable](#keep-wi-fi-connected-next-to-a-cable)).
- **The log:** `%APPDATA%\downloadmanagersquared\engine.log` (*Settings → About → Open log*), and *Copy diagnostics* in a download's Details. Attach them to an [issue](https://github.com/unicrncndms/downloadmanagersquared/issues) when reporting a problem. What they leave out is *Log privacy* in About: *Standard* (the default) leaves out passwords, links' paths, file names and your public IP and keeps the sites; *Increased* leaves out the sites too; *Off* writes everything. Lines already written stay as they were.
