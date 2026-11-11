# Download as folder frees the archive's bytes while it downloads

Date: 2026-11-06
Status: accepted

## Context

Extracting a large ZIP after downloading it needs room for the archive and its contents at once. DMS² writes every download into a sparse `.part` file, mostly out of order across several interfaces, so it can extract each Entry as soon as its bytes are there. The question was what to do with the archive's bytes afterwards: keep them until the end, or give their space back as the download goes.

## Decision

In a Download as folder, an Entry's compressed bytes are freed from the `.part` (`FSCTL_SET_ZERO_DATA`) as soon as it is extracted and its CRC32 checks out. Entries over 1 GB are also freed behind the decompressor while they are extracted. The archive never exists whole on disk, and the user never gets the `.zip`.

Integrity rests on each Entry's CRC32 and size from the central directory. A whole-file hash can't be checked, because the bytes are gone by the end. A hash the user typed therefore turns the mode off, and a server-sent digest is skipped with a note.

After a crash, an unfinished Entry is extracted again from its start, and any of its freed bytes are downloaded again. Freed ranges go back into the plan's remaining ranges. That is why the mode needs a server that supports ranges.

## Alternatives

- **Keep the archive until the end, then delete it.** That saves time but not space, which was the point.
- **Hash the bytes in order and free nothing past the hash position.** That keeps the whole-file hash, but ties freeing to in-order arrival, which multi-interface downloads don't give.
- **Free only whole Entries.** That's simpler and a crash never re-downloads anything, but an archive that is one huge Entry would save nothing.

## Consequences

- A Download as folder can't be turned back into a normal download once anything has been freed. The toggle locks at that point.
- A crash or a move to another drive can cost re-downloading up to one large Entry.
- The scheduler fetches a Download as folder front to back: every connection works within a few seconds' fetching of the first missing byte, so what waits on disk for the extraction stays small whatever the archive's size. The parts of a split archive that can only be read front to back (RAR volumes, a solid 7z) download one at a time for the same reason, after the end of the last part where that holds the list of files; a setting lets them download at once, at the cost of the space the parts fetched ahead take until they are extracted. Normal downloads keep the speed-based split.
- Anything that later wants the finished archive (a whole-file hash, seeding it, keeping a copy) doesn't work with this mode.
