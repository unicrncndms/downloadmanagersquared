# The exe that is elevated is held unchanged until it has started

Date: 2026-11-14
Status: accepted

## Context

Installing into Program Files runs the install's own `dms-engine.exe` as administrator with `--put-files` (ADR 0002). That exe sits in the folder the user extracted the zip to, which any process of the same user can write. A swap between the user starting the install and the UAC prompt being answered would run the replacement as administrator.

Copying the exe to a folder only administrators can write is not possible before elevating, since writing there needs elevation. A locked ACL on a per-run folder in the user's temp does not help: the user owns it and can change it. Checking a hash first leaves a gap between the check and the start.

## Decision

`putFilesElevated` opens the exe with `winutil.HoldUnchanged` (read access, share-read only) and keeps it open until the elevated process has exited. While the handle is open Windows refuses to write, replace or rename that file, or the folders above it, so the file that was run is the file the user started from.

## Consequences

- No copy, no hash and no extra folder.
- It does not help if the file was already swapped before the install started; that is the same user running code of their choosing, which PRIVACY.md says DMS² does not defend against.
- Only the exe that runs elevated is held; the files it copies (the host, notices) are read as data and run later as the user.
