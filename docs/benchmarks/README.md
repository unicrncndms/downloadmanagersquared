# Benchmarks

Results are in [results.csv](results.csv), one row per download. [bench.ps1](bench.ps1) writes them, so anyone can repeat a row on their own lines.

## What is measured

**Speed.** The same file from the same server, one after the other:

- each connection alone, one request (`curl --interface`);
- a conventional download manager over the Windows default connection: aria2 with 16 requests, IDM and JDownloader, each one that is installed, with its own settings;
- DMS² over every connection it is set to use. Its time runs from adding the link to the finished file, so it includes finding out what the server allows.

Speed is the file's size over that time. The default file is the Arch Linux ISO (1.3 GB) from kernel.org's mirror, a distribution server built for large downloads; the speed-test servers we tried first (OVH, Hetzner) cut off our lines; a server slower than your lines together measures the server, not DMS².

**Disk space.** The most space a drive loses while an archive becomes a folder: with *Download as folder*, and with a normal download followed by 7-Zip.

## Measuring speed

Nothing else should use the lines. Run each test three times; every run adds its rows. `-Unattended -Personal` runs everything with nobody watching.

```
cd docs\benchmarks
powershell -ExecutionPolicy Bypass -File bench.ps1
```

It measures each connection alone, then the other download managers, then asks you to add the link in DMS² and press Enter when it has finished (`-Unattended` does that step too). Delete DMS²'s downloaded file afterwards; the other tools' files are deleted by the script.

- A tool you timed by hand: `-Tool "Free Download Manager" -Seconds 84`
- Skip the other download managers: `-NoOthers`
- What the lines are: `-Note "250 Mbit cable + 5G hotspot"`

## The other download managers

Each runs without anyone watching, into its own folder under `%TEMP%`, and its time runs from handing it the link to the whole file on disk. A tool that is not installed is skipped with a line saying why.

- **aria2**: `aria2c` on the PATH. `-x16 -s16 -k1M`.
- **IDM**: found through its registry entry, or `-IDM <path to IDMan.exe>`. Started as `IDMan.exe /d <link> /p <folder> /f <name> /n`; IDM moves the file into the folder only once it is whole. Turn off IDM's "download complete" window (Options, Downloads) so runs don't wait on it. IDM must not be catching the browser's downloads while DMS² runs.
- **JDownloader**: needs JDownloader running with *Folder Watch* on (Settings, Extensions, Folder Watch). The script drops a `.crawljob` into JDownloader's `folderwatch` folder (found in its install folder, or `-JDownloader <folder>`); JDownloader adds and starts it, writes `name.part` and renames it when done. Its *max. chunks per download* setting is what it uses: set it to 8 or more, as JDownloader's default of 1 measures a single request. The run stays in JDownloader's list; remove it there.

The rows keep each tool's name. What the README and any summary show is *a conventional download manager*: the average of the tools' own averages (ADR 0012), so a tool run more often doesn't count more:

```
powershell -ExecutionPolicy Bypass -File bench.ps1 -Summary
```

It prints, per set of lines (`note`) and link, each line alone, a conventional download manager (and how many tools went into it), and DMS².

## Your own comparison

`-Personal` adds tools that also use every connection, for your own comparison only. Their rows go to `results-personal.csv`, which git ignores, and `-Summary` shows them by name from that file.

- **aria2 over every line**: `--multiple-interface` with each line's IPv4 address, needs two lines up.
- A tool with no command line, such as Plexo: time it by hand with `-Tool Plexo -Seconds 84 -Personal`.

## Without anyone watching

`-Unattended` replaces the DMS² step: the script adds the link through the running engine's pipe (the same calls the extension makes), waits for the download to finish, writes the row, and removes the download and its file from DMS². DMS² must be running, and nothing else downloading.

```
powershell -ExecutionPolicy Bypass -File bench.ps1 -Unattended
```

**A connection lost mid-download.** `-DropAfter 30` runs the DMS² step and switches one connection off in DMS²'s settings 30 seconds in, then switches it on again when the run ends. It saves a row `DMS² (line dropped)`, and the note says which connection and when. Compare it with the plain DMS² row from the same lines: the difference is what losing that line cost. `-DropLine "Wi-Fi"` names the connection (as DMS² shows it); without it the first one that is not the Windows default goes. Two connections must be enabled. This is DMS²'s own switch; an adapter unplugged or a tether going away is not simulated.

**Extraction without room.** `-Folder E:\bench -Url <a .zip, .7z or .rar link>` downloads it as a folder into that folder and records how it ends in the note: `outcome: completed`, `outcome: paused: Paused: not enough space on E: (needs ...)`, or `outcome: failed: ...`. The row is `disk`, with `peak_disk_bytes` the most space the drive lost. The script does not make the drive; it uses the one you name. DMS² keeps 256 MB free on every drive it writes to, so a drive must have that much on top of what the test needs. To test the space check (ADR 0006), point `-Folder` at a drive too small for what the archive extracts to plus those 256 MB: a small USB stick, or a VHD you have mounted yourself (Disk Management, Action, Create VHD; creating and mounting one needs admin once, using it does not). To test freeing as it extracts (ADR 0007), use a drive that holds the extracted files but not the archive and the extracted files together. A run that ends paused or failed is still saved, with its outcome.

## Measuring disk space

Use an empty drive or one nothing else writes to, and an archive of several GB. Run it once per tool:

```
powershell -ExecutionPolicy Bypass -File bench.ps1 -Disk E: -Tool "DMS² as folder" -Url <the archive's link>
```

Start the download to that drive, and press Enter when the folder is complete. For the comparison, delete the folder and run it again with `-Tool "Firefox + 7-Zip"`: download the archive normally, extract it with 7-Zip, then press Enter.

## The columns

`date`, `dms_version`, `test` (speed or disk), `tool`, `lines` (the connection, or how many DMS² used), `size_bytes`, `seconds`, `mbit_s`, `peak_disk_bytes`, `url`, `note`. Delete a row from the CSV to drop a run that went wrong.
