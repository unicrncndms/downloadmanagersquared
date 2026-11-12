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
