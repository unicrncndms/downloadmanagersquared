# Using DMS²

Download a file of 100 MB or more as usual, or use *Download with DMS²* on a link's menu.

- The toolbar icon lists your downloads. A left click on the tray icon opens the same list in its own window.
- Links on the clipboard: paste them anywhere in the list, or use *Add link from clipboard* in the tray menu (links already in the list are skipped).
- Each download shows its progress, speed and the connections it uses. **Details** has the rest.
- **Connections & settings** is at the bottom of the list.
- Unfinished files end in `.dms.part`. Progress is saved every few seconds.

### Sites with a link per IP

The first time, a window opens with a bar at the top ("DMS² is watching"). Click through the site until the download starts. DMS² remembers those clicks and repeats them by itself for each connection from then on.

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

### FTP and FTPS

Paste an `ftp://` or `ftps://` link into **Add download**; a link clicked on a page is taken too, as Firefox no longer opens them. The server's `SIZE` and `MDTM` give the size and version, and each connection logs in on its own and fetches a part with `REST` and `RETR`, over the connections you switched on (and the VPN, as set). Login is anonymous unless the link has one (`ftp://name:password@host/file`): the password is kept encrypted with Windows and is not shown or logged. A server that allows only one login at a time, or does not support `REST`, is downloaded over one connection. `ftp://` sends the password and the file unencrypted; `ftps://` is encrypted, with a certificate that must be trusted (port 990 is implicit TLS, any other port asks for it with `AUTH TLS`). The data connection always goes to the address the server was reached at, whatever it names in its passive reply.

### SFTP

Paste an `sftp://user@host/path` link into **Add download**, with the password in it (`sftp://user:password@host/path`) or, under Options, the path of a private key file and its passphrase. Each connection opens an SSH session of its own and reads its part of the file, over the connections you switched on (and the VPN, as set); the file's size and modification time are its version. The login is kept encrypted with Windows per host and user, so a later link `sftp://user@host/other` needs none; **Settings > SFTP logins** lists them (never the secret) and removes them. The first time a server is met, its row asks whether to trust the key's SHA256 fingerprint (**Trust**, or remove the download): nothing, not even the login, is sent before that. A server whose key later differs fails the link with a warning and is never accepted by itself: if you are sure the key really changed, **Settings > SFTP logins** lists the trusted keys and **Forget** removes one, so the next link asks again. A server that refuses more sessions or connections costs a connection, not the link.

### Checksums

A download's **Details → Checks → Edit** takes a SHA-256 or MD5. DMS² checks the file when it finishes (or right away if it already has), and a download that doesn't match is kept as `.dms.part` instead of finishing. Files from Google Cloud Storage (and sites served from it) carry their MD5, which is checked the same way when you haven't entered one.

## Settings

- **Connections:** turn each on or off, set its parallel requests (4) and speed limit, and choose the main connection, used alone for servers that can't split a file. VPN adapters start off. *Enable IPv6 (experimental)* (off) uses IPv6 too; each connection shows a badge with what it has: IPv4, IPv6 or IPv4 + IPv6.
- **Queue window:** optionally, per weekday, the hours the queue runs (an end before the start runs past midnight). Outside it nothing starts, running downloads pause and seeding torrents stop; they resume when it begins. A download you start or resume yourself still runs. No day set: no limit.
- **Saving and speed:** takeover size (100 MB), sites never taken over, save folder, downloads at once (1), notifications, keep the PC awake, start with Windows.
- **Speed:** the unit speeds are shown in (sizes and speeds use base 1000: 1 MB = 1,000,000 bytes), and a download and an upload limit for all connections together (none).
- **Archives:** Download as folder for new archives (off), a split archive's parts at once (off), keep the archive (Automatic).
- **Torrents:** seed ratio (1), port for incoming peers.
- **SFTP logins:** the logins and trusted server keys, removable.
- **Learned sites:** the clicks remembered per site. *Forget* makes DMS² ask again.
- **Appearance:** theme and accent colour.

### VPN

Each connection can get its own WireGuard tunnel, for downloads, torrents or both. Choose **Off** (the connections without a tunnel only), **VPN and direct** (the tunnels too) or **VPN only** (the tunnels only).

- **Private Internet Access:** sign in, and every connection without its own `.conf` gets a tunnel.
- **Other providers:** import a `.conf` per connection (it is kept encrypted with Windows). It must be plain WireGuard with an IPv4 address and an IPv4 DNS server (`DNS =`; IPv6 ones aren't used), one config per connection (a separate device or key at your provider). Obfuscated AmneziaWG configs don't work. If the provider forwards ports by NAT-PMP (Proton VPN on P2P servers, with NAT-PMP on when you make the config), DMS² gets a port for torrents by itself (not yet tested with Proton's servers). For a fixed port from your provider's panel (AirVPN), type it in the **Port** box next to the `.conf`.
- For sites with a link per IP, use a different server for each connection, or they may share one address.
- Which providers offer `.conf` files, and why PIA works differently: [vpn-providers.md](vpn-providers.md).

### Several browsers

Settings and the download list live in DMS² itself, so every browser shares them. Learned sites are kept per browser, and a download is always clicked through in the browser it came from.

## Upgrade and uninstall

**Upgrade:** run `dms-engine.exe` from a newer zip. It updates the existing install and keeps everything. The extension updates itself from this repository's releases, signed by Mozilla; to decide yourself, set **Allow automatic updates** to **Off** for it in `about:addons`.

**Uninstall:** *Settings → Apps → Installed apps → downloadmanagersquared*. Downloaded files are always kept; you choose whether settings and the download list go. Then remove the extension in your browser.
