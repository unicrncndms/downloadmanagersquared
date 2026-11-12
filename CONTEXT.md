# DMS²

A download manager that fetches one file over several network interfaces at once, so their bandwidth adds up.

## Language

### Fetching

**Interface**:
A network adapter the file can be fetched through, such as Wi-Fi, Ethernet or a phone tether, over IPv4, IPv6 or both (IPv6 only with the experimental setting on). Users see it called a "connection", since "interface" means little to them.
_Avoid_: Connection (except in user-facing text), adapter, route

**Main interface**:
The interface a download uses when it can't be split. The user chooses it; otherwise it is the Windows default route. When its probe succeeds, it decides what the file is; other interfaces must match it.
_Avoid_: Main connection (except in user-facing text), primary

**Connection**:
One HTTP request fetching part of the file over one interface, or one of its tunnels. An interface carries several connections at once.
_Avoid_: Stream, worker, thread, peer

**Chunk**:
A contiguous byte range of the file that one connection fetches.
_Avoid_: Segment, part, piece

**Backoff**:
An interface opening fewer connections, or none for a pause, because the server pushed back or the network faltered. Its link stays ready. Each push-back costs one connection, and each quiet two minutes gives one back. Users see it as "Server busy".
_Avoid_: Throttle, cooldown

### Links

**Source URL**:
The URL the user pasted or clicked to start a download. It may point at the file or at a landing page.
_Avoid_: Link, original link

**Link**:
One interface's working access to the file: a URL plus the cookie and referer it needs.
_Avoid_: URL (when the cookie and referer matter)

**Probe**:
The first request over a link, which learns the file's size and whether it supports ranges, and catches a mismatch.
_Avoid_: Check, test request

**Mismatch**:
A link that turns out to serve a different file, so its interface is left out of the download.

**Mirror**:
Another server with the same file, listed by a Metalink or by the server's `Link: rel=duplicate` header. Each interface's link uses one mirror. Mirrors are matched by size and checksum, and a mirror that serves another file or refuses it is dropped.
_Avoid_: Source, alternate

**Retrying**:
A link the engine will try again by itself after a pause. An unreachable interface retries indefinitely; a repeated HTTP error becomes a failed link after five tries, unless data arrives in between. A server that pushes back sets the pause and costs no try. An engine restart retries it at once.

**Needs landing page**:
A link that has to be resolved, but no landing page is known yet, so it waits for the user to give one.

**Failed link**:
A link the engine has given up on, for example after three resolves or because its server refuses ranges. Only the user's retry brings it back.
_Avoid_: Failed (for links that are retrying or waiting on the user)

### Resolving

**Landing page**:
A web page in front of the file whose download URL works only for the interface that opened it.
_Avoid_: Download page, gate

**Resolving**:
Getting a link for one interface by loading the landing page through that interface in the browser.
_Avoid_: Job (an internal word for one resolving attempt)

**Recipe**:
The clicks recorded on a landing-page host that lead to the download, each with how long the site made you wait before it, plus the wait for the download after the last click. Saved so later visits can be automated.
_Avoid_: Script, macro

**Wait**:
The time a landing page makes you wait before a step can be clicked, or before the download starts.
_Avoid_: Timer, countdown

**Recording**:
The first visit to a landing-page host, where the user clicks through by hand and the clicks become its recipe.

**Replay**:
A later visit, where the recipe's clicks are repeated automatically.

### Queue

**Queue**:
The downloads list in the order added, or as the user moved them, deciding which ones run. HTTP downloads go first; a torrent gives its slot to a queued HTTP download. A queued download the user moves before a running one takes its slot, and that one queues next behind it.

**Slot**:
One of the *Max parallel* places a running download holds. Seeding and fetching a torrent's metadata hold none.

### Torrents

**Torrent**:
A download fetched from a BitTorrent swarm instead of an HTTP server, started from a magnet link or a `.torrent` file. It sits in the same downloads list and queue as HTTP downloads, but always queues behind them.

**Peer**:
Another BitTorrent client in the swarm that the engine exchanges pieces with. Each peer is reached over one interface or one of its tunnels.
_Avoid_: Connection (that is an HTTP request)

**Seeding**:
A torrent uploading to peers after its own download is complete. It holds no queue slot, and only interfaces with upload on take part.

**Ratio**:
How much a torrent has uploaded divided by its size. Seeding stops when the ratio target is reached (1.0 by default) and the torrent is finished.

### Archives

**Download as folder**:
A ZIP, 7z or RAR download that is extracted while it downloads, so the user ends up with the extracted folder and never the archive. The archive's bytes are thrown away once the files in them are extracted.
_Avoid_: Unpack in place, streaming unzip, auto-extract

**Archive keeping**:
What a Download as folder does with its archive's bytes: free them as it extracts, keep them until it is extracted (interruption recovery: an update, reboot or crash then downloads nothing again), or keep the archive afterwards. Automatic, the default, keeps it until extracted when the drive has room, and frees it otherwise.
_Avoid_: Backup, cache

**Entry**:
One file or folder stored inside a ZIP archive. In a 7z, an Entry is a block: packed bytes that decompress into one or more files, front to back. A RAR is one Entry: the whole archive, as it has no list of files to read first.
_Avoid_: File (that is the download), member, item

**Extraction**:
One archive being extracted while its bytes download, whether it is one download or a Set's parts. An Entry that comes out wrong is fetched again once; wrong a second time, the downloads holding it pause.
_Avoid_: Unpacking, unzip

**Set**:
The parts of one split archive (x.7z.001…, RAR volumes x.part1.rar… or x.rar, x.r00…, split ZIPs x.z01…, x.zip or x.zip.001…), each a download of its own, extracted as one archive into one folder. Each part completes once fetched and leaves its bytes to the Set, which frees them as it extracts and deletes the part once all of it is freed. A part not added stops extraction at that point: "Part 2 missing". As a Download as folder the parts have one switch, one folder and one password, held by part 1, and the Set is known by the folder it extracts into, not by where each part's file lies. Its parts queue in part order. Where the archive can only be read front to back (RAR volumes, a solid 7z), they download one at a time, after the end of the last part where that holds the list of files, unless the user lets them download at once. A RAR's volumes are laid out as their downloads are known and its decompressor waits for the next; a plain x.rar or x.zip joins a Set only beside its other parts.
_Avoid_: Multi-part download, volume group

### VPN

**Tunnel**:
A WireGuard connection to a VPN server, carried over one interface and run inside the engine. Each interface has at most one, with its own key. It shares its interface's settings and limits. Users see it as "VPN via <interface>".
_Avoid_: VPN interface, adapter

**Path**:
A route a download's traffic can take: a bare interface, or that interface's tunnel. VPN mode decides which paths a download may use.
_Avoid_: Route, link (a link is one interface's access to a file)

**Main path**:
The path a download treats as its main interface: the main interface itself, or under VPN only its tunnel, else any tunnel that is up.

**VPN mode**:
Which paths a download may use: Off (bare interfaces only), Alongside (tunnels and bare interfaces) or VPN only (tunnels only, and pause when none is up). Each download type has one, set by the user: changing it moves every unfinished download of that type at once. A download can start in another mode (chosen when adding it, or a torrent's VPN question) until the type's mode next changes.
_Avoid_: Kill switch (it is what VPN only does, not a separate setting)

**Provider**:
Where tunnel configs come from: imported WireGuard `.conf` files, or PIA, which generates one config per interface through its API. An interface with an imported config uses it; PIA serves the interfaces without one.

**Forwarded port**:
A port the VPN server forwards to a tunnel, so torrent peers can connect in ("reachable"). A PIA tunnel gets one through PIA's API. An imported tunnel gets one if its gateway answers NAT-PMP (Proton VPN does), or the user types in a port their provider forwards (AirVPN); otherwise it is outgoing only. A typed-in port shows as "forwarded", not "reachable": the engine can't check it. It belongs to the imported tunnel and goes when that tunnel is removed. A forwarded port lasts one tunnel start: a restarted tunnel gets a new one.

### Browser

**Capture**:
The extension taking a download away from the browser and handing it to the engine. It happens to every download at or above the capture threshold, and to every torrent file.
_Avoid_: Intercept, grab, take over (except in user-facing text)

### Engine

**Downloads window**:
The engine's own window, opened from the tray, with the downloads list and the settings. It works whether or not a browser is running, and closing it leaves the engine running in the tray.
_Avoid_: GUI, app, manager, popup (the popup is the extension's toolbar panel)
