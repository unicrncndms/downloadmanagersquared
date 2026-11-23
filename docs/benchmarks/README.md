# Benchmarks

Results are in [results.csv](results.csv), one row per download. [bench.ps1](bench.ps1) writes them, so anyone can repeat a row on their own lines.

## What is measured

**Speed.** The same file from the same server, one after the other:

- on each connection alone: one request (`curl --interface`), and a conventional download manager: aria2, IDM and JDownloader, each one that is installed;
- DMS² over every connection it is set to use, and (`-Unattended`) on each connection alone, the others switched off in its settings for the run.

aria2 is bound to the line's address. IDM and JDownloader can't be bound, so the script makes the line the Windows default for their runs (the lowest interface metric) and puts every metric back afterwards. That needs an elevated shell; without one they run only on the current default line, and the script says so.

Every tool makes 8 requests per line (`-Requests`), except curl's one: aria2 `-x8`, or `-x16` over two lines; DMS² with *Requests per connection* at 8 for the run (any line's own setting too, all put back afterwards); Plexo with 8 streams per network, so it doesn't add more; IDM and JDownloader with their own setting, which must be 8. Every row's note says the count, so runs at another count stay apart in `-Summary`.

Every tool is timed the same way: from the first MB in, by the lines' byte counters (sampled every 250 ms), until the tool is done: the whole file under its name with nothing still writing it (a virus scan reading it doesn't hold the time up), aria2 or curl exiting, DMS² or Plexo saying it finished. A tool's start-up and how it picks up the link (JDownloader's Folder Watch) don't count. What it does after the last byte, joining parts or moving the file, does. The console prints that time next to the time from handing over the link. Nothing else may use the lines meanwhile, as other traffic counts too. A run whose counters show nothing falls back to its wall time. Rows before 2026-11-24 used wall time, which favoured aria2.

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
- Redo one step whose run went wrong (a line dropped mid-run): `-Only lines|aria2|IDM|JDownloader|Plexo|DMS` (`DMS` needs `-Unattended`). Delete the bad row from the CSV first.
- What the lines are: `-Note "250 Mbit cable + 5G hotspot"`

## The other download managers

Each runs without anyone watching. Every tool, DMS² included, writes into its own subfolder of one folder, so all of them join and write on the same drive: `dms-bench` in DMS²'s download folder, or `-Dir <folder>` (`%TEMP%\dms-bench` when DMS² isn't running). curl writes nothing: it measures the line. A tool that is not installed is skipped with a line saying why.

- **aria2**: `aria2c` on the PATH. `-x8 -s8 -k1M`.
- **IDM**: found through its registry entry, or `-IDM <path to IDMan.exe>`. Started as `IDMan.exe /d <link> /p <folder> /f <name> /n`; IDM moves the file into the folder only once it is whole. From a shell IDM won't take a link from (a sandboxed app, such as Claude's desktop app), add `-IDMViaTask`: the link goes to IDM through a one-off scheduled task. Set IDM's *Default max. conn. number* to 8 (Options, Connection) and its *Temporary directory* to the same drive as the bench folder (Options, Save to), as IDM joins its parts from there; the script can read neither. Turn off IDM's "download complete" window (Options, Downloads) so runs don't wait on it. IDM must not be catching the browser's downloads while DMS² runs.
- **JDownloader**: needs JDownloader running with *Folder Watch* on (Settings, Extensions, Folder Watch). The script drops a `.crawljob` into JDownloader's `folderwatch` folder (found in its install folder, or `-JDownloader <folder>`); JDownloader adds and starts it, writes `name.part` and renames it when done. Its *max. chunks per download* setting is what it uses: set it to 8 (JDownloader's default is 1); the script skips JDownloader with a line saying so when it isn't. The run stays in JDownloader's list; remove it there.

The rows keep each tool's name. What the README and any summary show is *a conventional download manager*: the average of the tools' own averages (ADR 0012), so a tool run more often doesn't count more:

```
powershell -ExecutionPolicy Bypass -File bench.ps1 -Summary
```

It prints, per set of lines (`note`) and link: each line alone; a conventional download manager on each line (and how many tools went into it; `Windows default` for rows from before every line was run); DMS² over every line; DMS² on each line alone.

## Your own comparison

`-Personal` adds tools that also use every connection, for your own comparison only. Their rows go to `results-personal.csv`, which git ignores, and `-Summary` shows them by name and lines from that file.

- **aria2 over every line**: `--multiple-interface` with each line's IPv4 address, needs two lines up.
- **Plexo**: over every line, then on each line alone. Plexo has no command line, so the script starts it with Electron's remote debugging (`--remote-debugging-port=9339`) and calls the API of Plexo's own window (`probeUrl`, `startDownload`, `listHistory`), as Plexo's tests do. Quit Plexo first (tray icon, Quit): a Plexo already running can't open the port, and the script stops the one it started when done. Its finished runs stay in Plexo's history.
- Any other tool with no command line: time it by hand with `-Tool <name> -Seconds 84 -Personal`.

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
