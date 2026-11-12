# Download as folder frees the archive's bytes while it downloads

Date: 2026-11-06
Amended: 2026-11-11 (formats, integrity per format, split sets with `partsParallel` on by default, scheduling described as it is)
Status: accepted, amended by 0007 (keeping the archive is now a choice; freeing is one of its values)

## Context

Extracting a large archive after downloading it needs room for the archive and its contents at once. DMS² writes every download into a sparse `.part` file, mostly out of order across several interfaces, so it can extract each Entry as soon as its bytes are there. The question was what to do with the archive's bytes afterwards: keep them until the end, or give their space back as the download goes.

## Decision

In a Download as folder, an Entry's compressed bytes are freed from the `.part` (`FSCTL_SET_ZERO_DATA`) once the Entry is extracted and checked. The Entry is marked done only if the free succeeds; otherwise it is extracted again. The free also reaches up to 1 MB into done neighbours, so shared 64 KB allocation units go too. Entries over 1 GB are extracted as their bytes arrive in order, and bytes the decompressor has passed are freed in 64 MB steps while the Entry is still being extracted. The archive never exists whole on disk, and the user never gets the archive file.

This works for ZIP, 7z and RAR archives, and for split sets of them (`.z01`/`.zip`, `.zip.001`, `.7z.001`, `.part1.rar`/`.r00`). A split set's parts keep their `.part` files until the set is extracted, so the archive exists in parts there.

Integrity rests on each Entry's size from the archive and, where the archive gives one, its CRC32:
- ZIP: CRC32 and size from the central directory.
- 7z: CRC32 where the archive stores one. An entry without a CRC is checked by size only.
- RAR: checked by size only in DMS²'s own code. The RAR file headers carry a CRC, which the decoder library may check on read; not verified here.

A whole-file hash can't be checked, because the bytes are gone by the end. A hash the user typed therefore turns the mode off, and a server-sent digest is skipped with a note. For a large Entry being streamed, freed bytes are gone before that Entry's check runs. A failed check means the Entry is fetched again, and a second failure pauses the download.

After a crash, an unfinished Entry is extracted again from its start, and any of its freed bytes are downloaded again. Freed ranges go back into the plan's remaining ranges. That is why the mode needs a server that supports ranges.

## Scheduling

- Downloads in this mode fetch front to back. The first chunk claimed covers the archive's tail, so the central directory arrives first.
- A new connection takes a chunk of about 10 seconds of its own speed (`stride`, `plan.go`), at least `minSplit` (4 MB). The rest of the file stays free for the next connection. The cut moves onto an Entry boundary when one is within `minSplit`.
- The 3-second steal rule for slow connections is unchanged.
- Nothing slows the download when extraction falls behind. The only brake on disk use is the free-space pause below.
- The 10-second stride is a heuristic. It bounds waiting bytes by time, not by size. See Consequences.

## Disk space

- Before extraction starts, the output drive must hold the total size of the Entries still to extract, plus the largest Entry's compressed size, plus `diskReserve` (256 MB). Otherwise the download pauses with "not enough disk space" and downloads nothing.
- After that check, the only guard is a free-space check every 30 seconds that pauses below `diskReserve`.

## Split sets

- The `partsParallel` setting is on by default: every part downloads at once. Parts then share the connections, and the space the parts fetched ahead take until they are extracted counts against the disk.
- With `partsParallel` off, an archive that can only be read front to back (RAR, a solid 7z, or one with an Entry longer than a part) downloads one part at a time: the first unfetched part, plus the part holding the list of files, until that list is read. An archive whose Entries are each shorter than a part still downloads all parts at once, since it extracts wherever its bytes are.

## Alternatives

- **Keep the archive until the end, then delete it.** That saves time but not space, which was the point.
- **Hash the bytes in order and free nothing past the hash position.** That keeps the whole-file hash, but ties freeing to in-order arrival, which multi-interface downloads don't give.
- **Free only whole Entries.** That's simpler and a crash never re-downloads anything, but an archive that is one huge Entry would save nothing.

## Consequences

- A Download as folder can't be turned back into a normal download once any byte has been freed. The toggle locks at that point.
- A crash or a move to another drive can cost re-downloading up to one large Entry.
- Storage comes before speed in two places. With `partsParallel` off, a front-to-back split set can't use the other parts' connections until the list of files is read. The space check can pause a download that a normal download would have finished, because it needs the extracted size and not just the archive size.
- The 10-second chunks add range requests compared with a normal download, and they cap how far ahead the disk fills only indirectly, through speed. Not measured.
- Anything that later wants the finished archive (a whole-file hash, seeding it, keeping a copy) doesn't work with this mode.
