# Measures downloads for docs/benchmarks/README.md and appends each result to results.csv.
#
#   .\bench.ps1                                   speed: every line alone, aria2, IDM and JDownloader if installed, then DMS²
#   .\bench.ps1 -Unattended -Personal             the same with nobody watching, plus multi-connection tools into results-personal.csv
#   .\bench.ps1 -Summary                          DMS² against a conventional download manager (the average of aria2, IDM, JDownloader)
#   .\bench.ps1 -Tool IDM -Seconds 84             speed: a row for a tool you timed by hand
#   .\bench.ps1 -Disk E: -Tool "DMS² as folder"   disk: watches free space on E: until you press Enter
param(
  [string]$Url = 'https://mirrors.edge.kernel.org/archlinux/iso/latest/archlinux-x86_64.iso',
  [string]$Tool,
  [double]$Seconds,
  [string]$Disk,
  [string]$Note = '',
  [string]$Version,
  [switch]$Unattended,   # the DMS² step adds the link and waits, through the engine's pipe
  [double]$DropAfter,    # seconds in, turn one connection off in DMS²'s settings (-DropLine names it)
  [string]$DropLine,     # the connection to drop: its name in DMS²; default the first one that is not the default
  [string]$Folder,       # a folder to extract into (-Url a .zip, .7z or .rar): a drive with too little room tests the space check
  [double]$Timeout = 3600,
  [switch]$Personal,     # also tools that use every connection (aria2 over each line); their rows go to results-personal.csv, which git ignores
  [switch]$Summary,      # print the averages from the CSV and stop
  [string]$IDM,          # IDMan.exe, if not where IDM's registry entry says
  [string]$JDownloader,  # JDownloader's folder (the one with folderwatch\), if not found
  [switch]$NoOthers      # skip aria2, IDM and JDownloader
)
$ErrorActionPreference = 'Stop'
$csv = Join-Path $PSScriptRoot 'results.csv'
$personalCsv = Join-Path $PSScriptRoot 'results-personal.csv'
# The tools averaged as "a conventional download manager". Their rows keep their names; summaries show only the average.
$conventional = 'aria2 -x16', 'IDM', 'JDownloader'

if (-not $Version) {
  $Version = Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*', 'HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*' -ErrorAction SilentlyContinue |
    Where-Object DisplayName -like 'downloadmanagersquared*' | Select-Object -First 1 -ExpandProperty DisplayVersion
}
[Threading.Thread]::CurrentThread.CurrentCulture = 'en-US' # decimal points in the CSV on every PC

function Save($test, $tool, $lines, $size, $secs, $peak, $file = $csv) {
  $mbit = ''
  if ($size -and $secs) { $mbit = [math]::Round($size * 8 / $secs / 1e6, 1) }
  $row = [pscustomobject]@{
    date = (Get-Date -Format 'yyyy-MM-dd HH:mm'); dms_version = $Version; test = $test; tool = $tool
    lines = $lines; size_bytes = $size; seconds = [math]::Round($secs, 1); mbit_s = $mbit
    peak_disk_bytes = $peak; url = $Url; note = $Note
  }
  $row | Export-Csv $file -Append -NoTypeInformation -Encoding UTF8
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

# The engine's pipe: one JSON line per call, one answer carrying the same id (events in between are skipped).
$script:id = 0
function Connect-Dms {
  $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
  $pipe = New-Object IO.Pipes.NamedPipeClientStream '.', "downloadmanagersquared-$sid", 'InOut'
  try { $pipe.Connect(3000) } catch { throw 'DMS² is not running (no engine pipe). Start it first.' }
  $script:rd = New-Object IO.StreamReader $pipe, (New-Object Text.UTF8Encoding $false)
  $script:wr = New-Object IO.StreamWriter $pipe, (New-Object Text.UTF8Encoding $false)
  $script:wr.AutoFlush = $true
}
function Invoke-Dms($method, $params = @{}) {
  $script:id++
  $script:wr.WriteLine((@{ id = $script:id; method = $method; params = $params } | ConvertTo-Json -Compress -Depth 6))
  while ($true) {
    $line = $script:rd.ReadLine()
    if ($null -eq $line) { throw 'DMS² closed the pipe.' }
    $r = $line | ConvertFrom-Json
    if ($r.id -eq $script:id -and -not $r.event) {
      if ($r.error) { throw "DMS² $method : $($r.error)" }
      return $r.result
    }
  }
}
function Get-DmsState { Invoke-Dms getState }

# Adds $Url to DMS², waits until it has finished (or failed, or stopped with an error, or $Timeout),
# and returns what happened. $into: Download as folder into that folder. $drop: seconds until a line is switched off.
function Invoke-DmsRun($into, $drop) {
  Connect-Dms
  $add = @{ url = $Url }
  if ($into) { $add.folder = $true }
  $t0 = Get-Date
  $jid = (Invoke-Dms add $add).id
  if ($into) { Invoke-Dms setFolder @{ id = $jid; on = $true; into = $into } | Out-Null }
  $drive = if ($into) { $into.Substring(0, 1) }
  $free0 = if ($drive) { (Get-PSDrive $drive).Free }
  $minFree = $free0
  $dropped = $null; $dropNote = ''
  try {
    while ($true) {
      Start-Sleep -Seconds 1
      $st = Get-DmsState
      $d = $st.downloads | Where-Object id -eq $jid
      if (-not $d) { throw 'The download is gone from DMS².' }
      if ($drive) { $minFree = [math]::Min($minFree, (Get-PSDrive $drive).Free) }
      $el = ((Get-Date) - $t0).TotalSeconds
      if ($drop -and -not $dropped -and $el -ge $drop) {
        $ifs = @($st.interfaces | Where-Object { $_.present -and $_.enabled -and -not $_.vpn })
        $victim = if ($DropLine) { $ifs | Where-Object { $_.name -eq $DropLine -or $_.windowsName -eq $DropLine } | Select-Object -First 1 }
                  else { $ifs | Where-Object { -not $_.isDefault } | Select-Object -First 1 }
        if (-not $victim) { throw 'No connection to drop (give -DropLine, and have two connections enabled).' }
        Invoke-Dms setInterface @{ id = $victim.id; enabled = $false } | Out-Null
        $dropped = $victim; $dropNote = "dropped $($victim.name) after $([math]::Round($el)) s"
        Write-Host "  switched off $($victim.name) at $([math]::Round($el)) s"
      }
      $s = $d.status
      $err = [string]$d.error
      if ($s -eq 'completed') { $why = 'completed' }
      elseif ($s -eq 'failed') { $why = "failed: $err" }
      elseif ($s -eq 'paused' -and $err) { $why = "paused: $err" }
      elseif ($el -gt $Timeout) { $why = "timeout after $Timeout s ($s)" }
      if ($why) {
        $secs = if ($s -eq 'completed' -and $d.finished) { (([datetime]$d.finished) - ([datetime]$d.added)).TotalSeconds } else { $el }
        $peak = if ($drive) { $free0 - $minFree } else { '' }
        $note = (@($Note, $dropNote, "outcome: $why") | Where-Object { $_ }) -join '; '
        $res = [pscustomobject]@{ Done = ($s -eq 'completed'); Size = $d.size; Seconds = $secs; Links = @($d.links.PSObject.Properties).Count; Peak = $peak; Note = $note }
        Invoke-Dms remove @{ id = $jid; deleteFile = $true } | Out-Null # the benchmark's own file and row
        return $res
      }
    }
  } finally {
    # leave the user's lines as they were
    if ($dropped) { Invoke-Dms setInterface @{ id = $dropped.id; enabled = $true } | Out-Null }
  }
}

# Seconds from $t0 until $path is complete: its full size, and no program holding it open. $null after $Timeout.
function Wait-File($path, $size, $t0) {
  while (((Get-Date) - $t0).TotalSeconds -lt $Timeout) {
    if ((Test-Path $path) -and (Get-Item $path).Length -eq $size) {
      try { ([IO.File]::Open($path, 'Open', 'Read', 'None')).Close(); return ((Get-Date) - $t0).TotalSeconds } catch { }
    }
    Start-Sleep -Milliseconds 250
  }
  $null
}

# The file name the other tools save to, from the link: they all get the same one.
function Get-BenchName { $n = [IO.Path]::GetFileName(([uri]$Url).AbsolutePath); if ($n) { $n } else { 'dms-bench.bin' } }

# One timed aria2 run. Returns the seconds, or $null if the file did not come out whole.
function Invoke-Aria($size, [string[]]$extra) {
  $dir = Join-Path $env:TEMP 'dms-bench-aria2'; $name = Get-BenchName
  New-Item -ItemType Directory -Force $dir | Out-Null
  $secs = (Measure-Command { aria2c -x16 -s16 -k1M --file-allocation=none --allow-overwrite=true -q @extra -d $dir -o $name $Url }).TotalSeconds
  $ok = (Test-Path "$dir\$name") -and (Get-Item "$dir\$name").Length -eq $size
  Remove-Item $dir -Recurse -Force -ErrorAction SilentlyContinue
  if ($ok) { $secs } else { $null }
}

# IDM through its command line: /d the link, /p the folder, /f the name, /n no questions. IDM puts the
# file in /p only once it is whole, so its arrival is the end.
function Invoke-IDM($size) {
  $exe = $IDM
  if (-not $exe) { $exe = (Get-ItemProperty 'HKCU:\Software\DownloadManager' -Name ExePath -ErrorAction SilentlyContinue).ExePath }
  if (-not $exe) { $exe = "${env:ProgramFiles(x86)}\Internet Download Manager\IDMan.exe", "$env:ProgramFiles\Internet Download Manager\IDMan.exe" | Where-Object { Test-Path $_ } | Select-Object -First 1 }
  if (-not $exe -or -not (Test-Path $exe)) { return 'not installed' }
  $dir = Join-Path $env:TEMP 'dms-bench-idm'; $name = Get-BenchName
  Remove-Item $dir -Recurse -Force -ErrorAction SilentlyContinue
  New-Item -ItemType Directory -Force $dir | Out-Null
  $t0 = Get-Date
  & $exe /d $Url /p $dir /f $name /n | Out-Null
  $secs = Wait-File "$dir\$name" $size $t0
  Remove-Item $dir -Recurse -Force -ErrorAction SilentlyContinue
  if ($null -eq $secs) { "no whole file after $Timeout s" } else { $secs }
}

# JDownloader through Folder Watch: a .crawljob dropped into its folderwatch folder is added and started.
# JDownloader writes name.part and renames it when done, so the whole file under its own name is the end.
function Invoke-JDownloader($size) {
  $jd = $JDownloader
  if (-not $jd) {
    $jd = @((Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*', 'HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*', 'HKLM:\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\*' -ErrorAction SilentlyContinue |
      Where-Object DisplayName -like 'JDownloader*' | Select-Object -ExpandProperty InstallLocation -ErrorAction SilentlyContinue)
      "$env:LOCALAPPDATA\JDownloader 2.0", "$env:ProgramFiles\JDownloader") | Where-Object { $_ -and (Test-Path (Join-Path $_ 'folderwatch')) } | Select-Object -First 1
  }
  if (-not $jd) { return 'not installed, or Folder Watch is off (Settings, Extensions, Folder Watch)' }
  $watch = Join-Path $jd 'folderwatch'
  $dir = Join-Path $env:TEMP 'dms-bench-jd'; $name = Get-BenchName
  Remove-Item $dir -Recurse -Force -ErrorAction SilentlyContinue
  New-Item -ItemType Directory -Force $dir | Out-Null
  $job = Join-Path $watch "dms-bench-$(Get-Date -Format yyyyMMddHHmmss).crawljob"
  $t0 = Get-Date
  @("text=$Url", "downloadFolder=$dir", "filename=$name", 'packageName=dms-bench', 'enabled=TRUE',
    'autoConfirm=TRUE', 'autoStart=TRUE', 'forcedStart=TRUE') | Set-Content $job -Encoding ASCII
  while ((Test-Path $job) -and ((Get-Date) - $t0).TotalSeconds -lt 60) { Start-Sleep -Milliseconds 250 }
  if (Test-Path $job) { Remove-Item $job; return 'JDownloader did not take the job within 60 s: is it running, with Folder Watch on?' }
  $secs = Wait-File "$dir\$name" $size $t0
  Remove-Item $dir -Recurse -Force -ErrorAction SilentlyContinue
  if ($null -eq $secs) { "no whole file after $Timeout s" } else { $secs }
}

# Per set of lines (note) and link: each line alone, DMS², and a conventional download manager as the
# average of the conventional tools' own averages, so a tool run more often doesn't count more.
if ($Summary) {
  $avg = { param($rows) $rows = @($rows | Where-Object { $_ }); if ($rows.Count) { [math]::Round(($rows | Measure-Object -Property mbit_s -Average).Average, 1) } }
  foreach ($file in $csv, $personalCsv) {
    if (-not (Test-Path $file)) { continue }
    $own = $file -eq $personalCsv
    Write-Host "`n$(Split-Path $file -Leaf)"
    Import-Csv $file | Where-Object { $_.test -eq 'speed' -and $_.mbit_s } | ForEach-Object { $_.mbit_s = [double]$_.mbit_s; $_ } |
      Group-Object note, url | ForEach-Object {
        $g = $_.Group
        $out = [ordered]@{ lines = $g[0].note; url = $g[0].url }
        $g | Where-Object tool -eq 'one request (curl)' | Group-Object lines | ForEach-Object { $out["alone: $($_.Name)"] = & $avg $_.Group }
        $conv = @($g | Where-Object { $conventional -contains $_.tool } | Group-Object tool | ForEach-Object { & $avg $_.Group })
        if ($conv.Count) { $out['a conventional download manager'] = [math]::Round(($conv | Measure-Object -Average).Average, 1); $out['tools averaged'] = $conv.Count }
        $d = & $avg ($g | Where-Object tool -eq 'DMS²'); if ($null -ne $d) { $out['DMS²'] = $d }
        if ($own) { $g | Where-Object { $_.tool -ne 'one request (curl)' -and $_.tool -ne 'DMS²' } | Group-Object tool | ForEach-Object { $out[$_.Name] = & $avg $_.Group } }
        [pscustomobject]$out
      } | Format-List | Out-Host
  }
  return
}

if ($Folder) {
  if (-not $PSBoundParameters.ContainsKey('Url')) { throw 'Give -Url: a .zip, .7z or .rar link.' }
  New-Item -ItemType Directory -Force $Folder | Out-Null
  Write-Host "DMS² as folder into $Folder"
  $r = Invoke-DmsRun $Folder $null
  $Note = $r.Note
  Save 'disk' 'DMS² as folder' "$($r.Links) connections" $r.Size $r.Seconds $r.Peak
  return
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

if ($DropAfter) {
  Write-Host "DMS², one connection switched off after $DropAfter s"
  $r = Invoke-DmsRun $null $DropAfter
  $Note = $r.Note
  if ($r.Done) { Save 'speed' 'DMS² (line dropped)' "$($r.Links) connections" $r.Size $r.Seconds '' }
  else { Write-Warning "Not finished: $($r.Note)" }
  return
}

if ($Tool) {
  if (-not $Seconds) { throw 'Give -Seconds: how long the download took.' }
  Save 'speed' $Tool '' $size $Seconds '' $(if ($Personal) { $personalCsv } else { $csv })
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

$aria = Get-Command aria2c -ErrorAction SilentlyContinue
if (-not $NoOthers) {
  # Conventional download managers, each over the Windows default connection with its own settings.
  if ($aria) {
    Write-Host 'aria2 -x16 over the Windows default connection'
    $secs = Invoke-Aria $size @()
    if ($secs) { Save 'speed' 'aria2 -x16' 'Windows default' $size $secs '' } else { Write-Warning 'aria2 did not finish the file, not saved' }
  } else { Write-Host 'aria2: not installed (aria2c not on PATH), skipped' }
  Write-Host 'IDM over the Windows default connection'
  $r = Invoke-IDM $size
  if ($r -is [double]) { Save 'speed' 'IDM' 'Windows default' $size $r '' } else { Write-Host "  IDM: $r, skipped" }
  Write-Host 'JDownloader over the Windows default connection'
  $r = Invoke-JDownloader $size
  if ($r -is [double]) { Save 'speed' 'JDownloader' 'Windows default' $size $r '' } else { Write-Host "  JDownloader: $r, skipped" }
}

if ($Personal) {
  # Other tools that use every connection: for Robin's own comparison, kept out of results.csv.
  $ips = @($lines | ForEach-Object { @($_.IPv4Address)[0].IPAddress })
  if ($aria -and $ips.Count -ge 2) {
    Write-Host "aria2 -x16 over every line ($($ips -join ', '))"
    $secs = Invoke-Aria $size @("--multiple-interface=$($ips -join ',')")
    if ($secs) { Save 'speed' 'aria2 -x16, every line' (@($lines.InterfaceAlias) -join ' + ') $size $secs '' $personalCsv }
    else { Write-Warning 'aria2 over every line did not finish the file, not saved' }
  } else { Write-Host 'aria2 over every line: needs aria2 and two lines up, skipped' }
}

if ($Unattended) {
  $r = Invoke-DmsRun $null $null
  if ($r.Done) { Save 'speed' 'DMS²' "$($r.Links) connections" $r.Size $r.Seconds '' }
  else { Write-Warning "Not finished: $($r.Note)" }
  return
}

$since = Get-Date
Write-Host "`nNow DMS²: make sure nothing else is downloading, then + Add this link:`n  $Url"
Read-Host 'Press Enter when DMS² shows it as finished' | Out-Null
$run = Get-DmsRun $Url $since
if ($run) { Save 'speed' 'DMS²' "$($run.Links) connections" $run.Size $run.Seconds '' }
else { Write-Warning "No finished DMS² download of that link since $since. Nothing saved for DMS²." }
