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
One HTTP request fetching part of the file over one interface. An interface carries several connections at once.
_Avoid_: Stream, worker, thread

**Chunk**:
A contiguous byte range of the file that one connection fetches.
_Avoid_: Segment, part, piece

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
A link the engine will try again by itself after a pause. An unreachable interface retries indefinitely; a repeated HTTP error becomes a failed link after five tries. An engine restart retries it at once.

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
