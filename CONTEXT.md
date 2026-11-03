# DMS²

A download manager that fetches one file over several network interfaces at once, so their bandwidth adds up.

## Language

### Fetching

**Interface**:
A network adapter the file can be fetched through, such as Wi-Fi, Ethernet or a phone tether. Users see it called a "connection", since "interface" means little to them.
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
The clicks recorded on a landing-page host that lead to the download, saved so later visits can be automated.
_Avoid_: Script, macro

**Recording**:
The first visit to a landing-page host, where the user clicks through by hand and the clicks become its recipe.

**Replay**:
A later visit, where the recipe's clicks are repeated automatically.

### Queue

**Queue**:
The downloads list in the order added, deciding which ones run. HTTP downloads go first; a torrent gives its slot to a queued HTTP download.

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
Which paths a download may use: Off (bare interfaces only), Alongside (tunnels and bare interfaces) or VPN only (tunnels only, and pause when none is up). Each download type has a default, and a download's mode is fixed when it is added.
_Avoid_: Kill switch (it is what VPN only does, not a separate setting)

**Provider**:
Where tunnel configs come from: imported WireGuard `.conf` files, or PIA, which generates one config per interface through its API.
