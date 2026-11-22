# Benchmarks

Results are in [results.csv](results.csv), one row per download. [bench.ps1](bench.ps1) writes them, so anyone can repeat a row on their own lines.

## What is measured

**Speed.** The same file from the same server, one after the other:

- each connection alone, one request (`curl --interface`);
- aria2 with 16 requests over the Windows default connection, if `aria2c` is installed;
- DMS² over every connection it is set to use. Its time runs from adding the link to the finished file, so it includes finding out what the server allows.

Speed is the file's size over that time. The default file is the Arch Linux ISO (1.3 GB) from kernel.org's mirror, a distribution server built for large downloads; the speed-test servers we tried first (OVH, Hetzner) cut off our lines; a server slower than your lines together measures the server, not DMS².

**Disk space.** The most space a drive loses while an archive becomes a folder: with *Download as folder*, and with a normal download followed by 7-Zip.

## Measuring speed

Nothing else should use the lines. Run each test three times; every run adds its rows.

```
cd docs\benchmarks
powershell -ExecutionPolicy Bypass -File bench.ps1
```

It measures each connection alone, then asks you to add the link in DMS² and press Enter when it has finished. Delete the downloaded file afterwards.

- A tool you timed by hand (IDM, Free Download Manager, JDownloader): `-Tool IDM -Seconds 84`
- What the lines are: `-Note "250 Mbit cable + 5G hotspot"`

## Without anyone watching

`-Unattended` replaces the DMS² step: the script adds the link through the running engine's pipe (the same calls the extension makes), waits for the download to finish, writes the row, and removes the download and its file from DMS². DMS² must be running, and nothing else downloading.

```
powershell -ExecutionPolicy Bypass -File bench.ps1 -Unattended
```

**A connection lost mid-download.** `-DropAfter 30` runs the DMS² step and switches one connection off in DMS²'s settings 30 seconds in, then switches it on again when the run ends. It saves a row `DMS² (line dropped)`, and the note says which connection and when. Compare it with the plain DMS² row from the same lines: the difference is what losing that line cost. `-DropLine "Wi-Fi"` names the connection (as DMS² shows it); without it the first one that is not the Windows default goes. Two connections must be enabled. This is DMS²'s own switch; an adapter unplugged or a tether going away is not simulated.

**Extraction without room.** `-Folder E:\bench -Url <a .zip, .7z or .rar link>` downloads it as a folder into that folder and records how it ends in the note: `outcome: completed`, `outcome: paused: Paused: not enough space on E: (needs ...)`, or `outcome: failed: ...`. The row is `disk`, with `peak_disk_bytes` the most space the drive lost. The script does not make the drive; it uses the one you name. To test the space check (ADR 0006), point `-Folder` at a drive too small for what the archive extracts to: a small USB stick, or a VHD you have mounted yourself (Disk Management, Action, Create VHD; creating and mounting one needs admin once, using it does not). To test freeing as it extracts (ADR 0007), use a drive that holds the extracted files but not the archive and the extracted files together. A run that ends paused or failed is still saved, with its outcome.

## Measuring disk space

Use an empty drive or one nothing else writes to, and an archive of several GB. Run it once per tool:

```
powershell -ExecutionPolicy Bypass -File bench.ps1 -Disk E: -Tool "DMS² as folder" -Url <the archive's link>
```

Start the download to that drive, and press Enter when the folder is complete. For the comparison, delete the folder and run it again with `-Tool "Firefox + 7-Zip"`: download the archive normally, extract it with 7-Zip, then press Enter.

## The columns

`date`, `dms_version`, `test` (speed or disk), `tool`, `lines` (the connection, or how many DMS² used), `size_bytes`, `seconds`, `mbit_s`, `peak_disk_bytes`, `url`, `note`. Delete a row from the CSV to drop a run that went wrong.
