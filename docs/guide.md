# Using DMS²

Download a file of 100 MB or more as usual, or use *Download with DMS²* on a link's menu.

- The toolbar icon lists your downloads. A left click on the tray icon opens the same list in its own window.
- Each download shows its progress, speed and the connections it uses. **Details** has the rest.
- **Connections & settings** is at the bottom of the list.
- Unfinished files end in `.dms.part`. Progress is saved every few seconds.

### Sites with a link per IP

The first time, a window opens with a blue bar. Click through the site until the download starts. DMS² remembers those clicks and repeats them by itself for each connection from then on.

These windows start without cookies, so sites that need you logged in can't be clicked through. Many file hosts limit free downloads per IP address, and their terms may not allow getting around that. Following each host's terms is up to you.

### Download as folder

Switch it on with the box when adding an archive link, with the switch on a download's card, or for every new archive in the settings.

- DMS² fetches the archive's list of files first, then downloads front to back and extracts each file as soon as its bytes are there. A second bar on the card shows the extraction, what it waits for, and the time left.
- Every file is checked against the archive's own checksum. One that comes out wrong is downloaded again.
- You can change the folder it extracts into while it runs, on the same drive or another.
- **Split archives:** add all the parts. They show as one card with one switch, download in the order that keeps the least on disk, and extract as one archive. A part that isn't added stops extraction there: "Part 2 missing". *Download a split archive's parts at once* in the settings trades disk space for speed.
- **Passwords:** an encrypted 7z or RAR asks for its password on the card and keeps downloading meanwhile. An encrypted ZIP is downloaded as a normal file.
- It needs a server that allows split downloads. A ZIP must be compressed with Stored, Deflate, Deflate64 or Zstandard. Anything it can't extract is downloaded as a normal file, with the reason in Details.
- **Keeping the archive:** by default (*Automatic*) the archive is kept until it is extracted when the drive has room for it, so an update, reboot or crash while extracting downloads nothing again; on a nearly full drive its space is freed as it extracts. Details can set it per download: *Free while extracting*, *Keep until extracted*, or *Keep the archive* to have the archive file too ([ADR 0007](adr/0007-keeping-the-archive-for-interruption-recovery.md)).
- There is no whole archive to check at the end, so a checksum you entered turns the mode off.

### Checksums

A download's **Details → Checks → Edit** takes a SHA-256 or MD5. DMS² checks the file when it finishes (or right away if it already has), and a download that doesn't match is kept as `.dms.part` instead of finishing. Files from Google Cloud Storage (and sites served from it) carry their MD5, which is checked the same way when you haven't entered one.

## Settings

- **Connections:** turn each on or off, set its parallel requests (4) and speed limit, and choose the main connection, used alone for servers that can't split a file. VPN adapters start off. *Enable IPv6 (experimental)* (off) uses IPv6 too; each connection shows a badge with what it has: IPv4, IPv6 or IPv4 + IPv6.
- **Downloads:** takeover size (100 MB), save folder, downloads at once (1), Download as folder for new archives (off), a split archive's parts at once (off), keep the archive (Automatic).
- **Torrents:** seed ratio (1).
- **Startup:** start with Windows.
- **Learned sites:** the clicks remembered per site. *Forget* makes DMS² ask again.

### VPN

Each connection can get its own WireGuard tunnel, for downloads, torrents or both. Choose whether they use the bare connections, the tunnels too, or only the tunnels.

- **Private Internet Access:** sign in, and every connection without its own `.conf` gets a tunnel.
- **Other providers:** import a `.conf` per connection. It must be plain WireGuard with an IPv4 address and an IPv4 DNS server (`DNS =`; IPv6 ones aren't used), one config per connection (a separate device or key at your provider). Obfuscated AmneziaWG configs don't work. If the provider forwards ports by NAT-PMP (Proton VPN on P2P servers, with NAT-PMP on when you make the config), DMS² gets a port for torrents by itself (not yet tested with Proton's servers). For a fixed port from your provider's panel (AirVPN), type it in the **Port** box next to the `.conf`.
- For sites with a link per IP, use a different server for each connection, or they may share one address.
- Which providers offer `.conf` files, and why PIA works differently: [vpn-providers.md](vpn-providers.md).

### Several browsers

Settings and the download list live in DMS² itself, so every browser shares them. Learned sites are kept per browser, and a download is always clicked through in the browser it came from.

## Upgrade and uninstall

**Upgrade:** run `dms-engine.exe` from a newer zip. It updates the existing install and keeps everything. The extension updates itself from this repository's releases, signed by Mozilla; to decide yourself, set **Allow automatic updates** to **Off** for it in `about:addons`.

**Uninstall:** *Settings → Apps → Installed apps → downloadmanagersquared*. Downloaded files are always kept; you choose whether settings and the download list go. Then remove the extension in your browser.
