# Measures downloads for docs/benchmarks/README.md and appends each result to results.csv.
#
#   .\bench.ps1                                   speed: every line alone, aria2 if installed, then DMS²
#   .\bench.ps1 -Tool IDM -Seconds 84             speed: a row for a tool you timed by hand
#   .\bench.ps1 -Disk E: -Tool "DMS² as folder"   disk: watches free space on E: until you press Enter
param(
  [string]$Url = 'https://mirrors.edge.kernel.org/archlinux/iso/latest/archlinux-x86_64.iso',
  [string]$Tool,
  [double]$Seconds,
  [string]$Disk,
  [string]$Note = '',
  [string]$Version
)
$ErrorActionPreference = 'Stop'
$csv = Join-Path $PSScriptRoot 'results.csv'

if (-not $Version) {
  $Version = Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*', 'HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*' -ErrorAction SilentlyContinue |
    Where-Object DisplayName -like 'downloadmanagersquared*' | Select-Object -First 1 -ExpandProperty DisplayVersion
}
[Threading.Thread]::CurrentThread.CurrentCulture = 'en-US' # decimal points in the CSV on every PC

function Save($test, $tool, $lines, $size, $secs, $peak) {
  $mbit = ''
  if ($size -and $secs) { $mbit = [math]::Round($size * 8 / $secs / 1e6, 1) }
  $row = [pscustomobject]@{
    date = (Get-Date -Format 'yyyy-MM-dd HH:mm'); dms_version = $Version; test = $test; tool = $tool
    lines = $lines; size_bytes = $size; seconds = [math]::Round($secs, 1); mbit_s = $mbit
    peak_disk_bytes = $peak; url = $Url; note = $Note
  }
  $row | Export-Csv $csv -Append -NoTypeInformation -Encoding UTF8
  Write-Host ("  saved: {0} | {1} | {2} s | {3} Mbit/s {4}" -f $tool, $lines, $row.seconds, $mbit, $peak)
}

# The newest completed DMS² download of $url added after $since: its size, seconds and connections.
function Get-DmsRun($url, $since) {
  Get-ChildItem "$env:APPDATA\downloadmanagersquared\downloads\*.json" | ForEach-Object {
    $j = Get-Content $_.FullName -Raw -Encoding UTF8 | ConvertFrom-Json
    # ConvertFrom-Json leaves the timestamps as strings in 5.1 and makes them dates in 7
    if ($j.url -eq $url -and $j.status -eq 'completed' -and $j.finished -and ([datetime]$j.added) -ge $since) {
      [pscustomobject]@{
        Size = $j.size; Seconds = (([datetime]$j.finished) - ([datetime]$j.added)).TotalSeconds
        Links = @($j.links.PSObject.Properties).Count; Added = [datetime]$j.added
      }
    }
  } | Sort-Object Added | Select-Object -Last 1
}

if ($Disk) {
  if (-not $Tool) { throw 'Give -Tool, for example -Tool "DMS² as folder" or -Tool "Firefox + 7-Zip".' }
  $drive = $Disk.TrimEnd(':', '\')
  $start = (Get-PSDrive $drive).Free
  $min = $start
  $t0 = Get-Date
  Write-Host "Watching free space on ${drive}:. Start the download now. Press Enter when the folder is complete."
  while (-not ([Console]::KeyAvailable -and [Console]::ReadKey($true).Key -eq 'Enter')) {
    $free = (Get-PSDrive $drive).Free
    if ($free -lt $min) { $min = $free }
    Write-Host -NoNewline ("`r  peak so far: {0:N1} GB   " -f (($start - $min) / 1GB))
    Start-Sleep -Milliseconds 500
  }
  Write-Host ''
  Save 'disk' $Tool '' '' ((Get-Date) - $t0).TotalSeconds ($start - $min)
  return
}

# A range request, because some servers (Hetzner) refuse HEAD.
$m = curl.exe -sL -r 0-1 -D - -o NUL $Url | Select-String '^content-range:\s*bytes 0-1/(\d+)' | Select-Object -Last 1
if (-not $m) { throw "No size from $Url (a server that answers neither a range request nor a size). Try another link." }
$size = [long]$m.Matches.Groups[1].Value

if ($Tool) {
  if (-not $Seconds) { throw 'Give -Seconds: how long the download took.' }
  Save 'speed' $Tool '' $size $Seconds ''
  return
}

# Windows sends a socket bound to an address out of that address's interface, so this is one line alone.
$lines = Get-NetIPConfiguration | Where-Object { $_.IPv4DefaultGateway -and $_.NetAdapter.Status -eq 'Up' }
foreach ($l in $lines) {
  $ip = @($l.IPv4Address)[0].IPAddress
  Write-Host "One connection alone: $($l.InterfaceAlias) ($ip)"
  $out = curl.exe --interface $ip -sL -o NUL -w '%{size_download} %{time_total}' $Url
  $got, $secs = $out -split ' '
  if ([long]$got -eq $size) { Save 'speed' 'one request (curl)' $l.InterfaceAlias $size ([double]$secs) '' }
  else { Write-Warning "$($l.InterfaceAlias): got $got of $size bytes, not saved" }
}

if (Get-Command aria2c -ErrorAction SilentlyContinue) {
  Write-Host 'aria2 -x16 over the Windows default connection'
  $tmp = Join-Path $env:TEMP 'dms-bench.tmp'
  $secs = (Measure-Command { aria2c -x16 -s16 -k1M --file-allocation=none --allow-overwrite=true -q -d (Split-Path $tmp) -o (Split-Path $tmp -Leaf) $Url }).TotalSeconds
  if ((Test-Path $tmp) -and (Get-Item $tmp).Length -eq $size) { Save 'speed' 'aria2 -x16' 'Windows default' $size $secs '' }
  else { Write-Warning 'aria2 did not finish the file, not saved' }
  Remove-Item $tmp -ErrorAction SilentlyContinue
}

$since = Get-Date
Write-Host "`nNow DMS²: make sure nothing else is downloading, then + Add this link:`n  $Url"
Read-Host 'Press Enter when DMS² shows it as finished' | Out-Null
$run = Get-DmsRun $Url $since
if ($run) { Save 'speed' 'DMS²' "$($run.Links) connections" $run.Size $run.Seconds '' }
else { Write-Warning "No finished DMS² download of that link since $since. Nothing saved for DMS²." }
