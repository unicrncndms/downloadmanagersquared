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

## Measuring disk space

Use an empty drive or one nothing else writes to, and an archive of several GB. Run it once per tool:

```
powershell -ExecutionPolicy Bypass -File bench.ps1 -Disk E: -Tool "DMS² as folder" -Url <the archive's link>
```

Start the download to that drive, and press Enter when the folder is complete. For the comparison, delete the folder and run it again with `-Tool "Firefox + 7-Zip"`: download the archive normally, extract it with 7-Zip, then press Enter.

## The columns

`date`, `dms_version`, `test` (speed or disk), `tool`, `lines` (the connection, or how many DMS² used), `size_bytes`, `seconds`, `mbit_s`, `peak_disk_bytes`, `url`, `note`. Delete a row from the CSV to drop a run that went wrong.
