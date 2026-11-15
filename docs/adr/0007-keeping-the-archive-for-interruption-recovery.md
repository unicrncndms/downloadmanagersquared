# A Download as folder can keep its archive, for interruption recovery or for good

Date: 2026-11-11
Status: accepted
Amends: 0006

## Context

ADR 0006 frees an archive's bytes as soon as they are extracted. For an Entry over 1 GB this happens while it is still being decompressed. The decompressor lives only in memory, so any stop ends it: an update, a reboot, a crash, even a clean quit. The next run has to decode that Entry again from its front, and every byte of it already freed is downloaded again.

A RAR is one Entry, and so is a RAR in volumes, so for them that means the whole archive. A 32 GB game in RAR volumes, interrupted by an update near its end, downloaded about 30 GB again. Saving the decompressor's state is not possible: a solid RAR or LZMA decoder holds up to 1 GB of dictionary, and neither library can save it. Keeping a fixed window (say the last 1 GB) behind the decompressor does not help either, because a restart decodes from the Entry's front.

Separately, users sometimes want the archive file as well as the extracted folder: to seed it, to check its whole-file hash, or to keep a copy.

## Decision

Each Download as folder has one choice for its archive, set per download and defaulted from a setting:

- **Automatic** (the default): keep the archive until it is extracted when the output drive has room for the whole archive, what it extracts to and `diskReserve`. Otherwise free as it extracts. The check is made when extraction starts. If free space later falls below `diskReserve` while the archive is kept, the download switches to freeing rather than pausing.
- **Free as it extracts**: ADR 0006 as written. It needs the least space, and an interruption downloads the freed bytes of the unfinished Entry again.
- **Keep until extracted** (interruption recovery): nothing is freed while extracting. A restart decodes the unfinished Entry again from bytes still on disk, and downloads nothing again. Once every Entry is extracted, the archive is deleted, and a Set's parts with it.
- **Keep the archive afterwards**: as Keep until extracted, but the archive stays, under its own name in the download's folder (a Set's parts under theirs). This is off unless chosen. The whole-file hash can be checked in this mode.

The choice can be changed until the first byte is freed. After that, Free as it extracts is fixed for that download, since the bytes are already gone.

## Consequences

- With Automatic, most downloads on a drive with room never download anything again after an interruption. A nearly full drive behaves as before.
- Keep until extracted and Keep afterwards need room for the archive plus its contents at once, which is what ADR 0006 set out to avoid. That is why this is a choice and not the rule.
- The space check before extraction (0006) counts the archive too when it is kept.
- 2b (warn before an update or Quit) still matters for downloads that free as they extract.

## Addendum 2026-11-15: ordered plans take over a slow front chunk, and stride in bigger steps

Three changes to how an ordered plan (a Download as folder) is fetched, from issue 29.

- **The tail guess is 4 MiB** (`archive.TailGuess`, was 64 KiB), the plan's `minSplit`, capped by the file size. 64 KiB held about 650 ZIP entries, so most archives needed a second request for the rest of the central directory, and that chunk waited for a free connection. The bytes are the file's own, written in place, and `Need` reads at most its search window into memory, so the larger guess costs no memory and no extra bytes on disk.
- **A slow front chunk is taken over.** Before, `claim` looked for a victim only when no free chunk was left, which in an ordered plan is the end of the file. A solid 7z or a RAR is read front to back, so one connection that slowed down stalled all extraction and freeing while the others fetched on, and the disk filled by speed x stall time. Now a connection with free chunks left still takes the far half of the slowest active chunk when that chunk lies at or before the lowest free one and has more than `orderedSteal` = 2 x `stride` = 20 s to go. A chunk is sized to about one stride of its connection's speed, so over two strides left means the connection runs at under half of what it was sized for. A lower threshold would split healthy chunks all the time and cost requests. Simulation (`TestOrderedStealsASlowFront`, 6 connections at 8 MiB/s, 2 GiB solid, front connection at 0.1x from 5 s): the first 128 MiB are on disk after 14 s, against 47 s without the takeover and 49 s in normal mode. Normal mode is unchanged (the code runs only when `ordered`).
- **The ordered stride has a floor of 32 MiB** (`orderedStride`): `max(speed x stride, 32 MiB)`, where the plan already leaves a chunk alone when less than `minSplit` would remain, so it is capped by the bytes left. A connection at 1 MB/s took 10 MB per request, now 32 MiB: about 3.4 times fewer range requests per GB for slow connections (not measured against a server; the first claims, with speed 0, took 4 MiB and now take 32 MiB). The cost is disk lag: the archive is freed later by at most one such stride per connection, N x 32 MiB, in the worst case when every connection is slow (6 connections: 192 MiB). The "ahead of the front" bound of ADR 0006 (two strides of the download's speed) still holds in the simulations, and held-back bytes on average rise by about 3 % for archives of small Entries. With Keep until extracted nothing is freed, so there is no cost.
