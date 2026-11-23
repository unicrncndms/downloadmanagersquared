# Measures downloads for docs/benchmarks/README.md and appends each result to results.csv.
#
#   .\bench.ps1                                   speed: on each line curl, aria2, IDM and JDownloader if installed, then DMS²
#                                                 (IDM and JDownloader on each line only from an elevated shell)
#   .\bench.ps1 -Unattended -Personal             the same with nobody watching, DMS² also on each line alone,
#                                                 plus multi-line tools (aria2 every line, Plexo) into results-personal.csv
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
  [string]$Dir,         # where every tool writes its file, one subfolder each; default dms-bench in DMS²'s download folder
  [int]$Requests = 8,    # requests per line for every tool that can be set: aria2, DMS², Plexo (IDM and JDownloader: their own settings, checked)
  [switch]$Personal,     # also tools that use every connection (aria2 over each line); their rows go to results-personal.csv, which git ignores
  [switch]$Summary,      # print the averages from the CSV and stop
  [string]$IDM,          # IDMan.exe, if not where IDM's registry entry says
  [switch]$IDMViaTask,   # hand IDM the link through a one-off scheduled task: for a shell IDM won't take links from (a sandboxed app)
  [string]$JDownloader,  # JDownloader's folder (the one with folderwatch\), if not found
  [switch]$NoOthers,     # skip aria2, IDM and JDownloader
  [ValidateSet('lines', 'aria2', 'IDM', 'JDownloader', 'Plexo', 'DMS')]
  [string]$Only          # one step alone, to redo a run that went wrong (DMS: with -Unattended)
)
$ErrorActionPreference = 'Stop'
$csv = Join-Path $PSScriptRoot 'results.csv'
$personalCsv = Join-Path $PSScriptRoot 'results-personal.csv'
# The tools averaged as "a conventional download manager". Their rows keep their names; summaries show only the average.
$conventional = 'aria2', 'aria2 -x16', 'IDM', 'JDownloader' # aria2 -x16: rows from before -Requests

if (-not $Version) {
  $Version = Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*', 'HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*' -ErrorAction SilentlyContinue |
    Where-Object { $_.PSChildName -eq 'downloadmanagersquared' } | Select-Object -First 1 -ExpandProperty DisplayVersion # shown as DMS², keyed by the old name
}
[Threading.Thread]::CurrentThread.CurrentCulture = 'en-US' # decimal points in the CSV on every PC
if (-not $Summary) { $Note = (@($Note, "$Requests requests per line") | Where-Object { $_ }) -join '; ' } # keeps runs at other counts apart in -Summary

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

# Every tool is timed the same way: from the first MB in, by the lines' byte counters, until the tool is done
# (the whole file on disk, or the tool saying so). Its start-up and link pickup (Folder Watch) don't count;
# what it does after the last byte (joining parts, moving the file) does.
# $script:nicNames is set once the lines are known; without it the runs fall back to their own wall time.
$script:nicNames = @()
function Start-Meter { $script:meter = [Collections.Generic.List[object]]::new(); Step-Meter }
function Step-Meter {
  if (-not $script:nicNames.Count -or $null -eq $script:meter) { return }
  $b = 0
  foreach ($n in [Net.NetworkInformation.NetworkInterface]::GetAllNetworkInterfaces()) {
    if ($script:nicNames -contains $n.Name) { $b += $n.GetIPStatistics().BytesReceived }
  }
  $script:meter.Add([pscustomobject]@{ T = Get-Date; B = $b })
}
# Seconds from the first MB in since Start-Meter until $end (now if not given), or $wall when nothing was metered.
function Get-MeterSeconds($wall, $end = (Get-Date)) {
  Step-Meter
  $s = $script:meter; $script:meter = $null
  if (-not $s -or $s.Count -lt 3 -or ($s[-1].B - $s[0].B) -lt 4MB) { return $wall }
  $first = ($s | Where-Object { $_.B - $s[0].B -le 1MB } | Select-Object -Last 1).T
  $secs = ($end - $first).TotalSeconds
  Write-Host ("  from the first byte {0:N1} s, from the start {1:N1} s" -f $secs, $wall)
  $secs
}
function Wait-Meter($seconds) { $t = Get-Date; while (((Get-Date) - $t).TotalSeconds -lt $seconds) { Step-Meter; Start-Sleep -Milliseconds 250 } }

# The newest completed DMS² download of $url added after $since: its size, seconds and connections.
function Get-DmsRun($url, $since) {
  Get-ChildItem "$env:APPDATA\downloadmanagersquared\downloads\*.json" | ForEach-Object {
    $j = Get-Content $_.FullName -Raw -Encoding UTF8 | ConvertFrom-Json
    # ConvertFrom-Json leaves the timestamps as strings in 5.1 and makes them dates in 7
    if ($j.url -eq $url -and $j.status -eq 'completed' -and $j.finished -and ([datetime]$j.added) -ge $since) {
      [pscustomobject]@{
        Size = $j.size; Seconds = (([datetime]$j.finished) - ([datetime]$j.added)).TotalSeconds
        Links = @($j.links.PSObject.Properties).Count; Added = [datetime]$j.added; Finished = ([datetime]$j.finished).ToLocalTime()
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
# $alone: a line's IPv4 address; the other lines are switched off in DMS² for the run.
function Invoke-DmsRun($into, $drop, $alone) {
  Connect-Dms
  # $Requests per line for the run: the default and any line's own override, put back afterwards
  $was = Invoke-Dms getSettings
  $overrides = @($was.interfaces.PSObject.Properties | Where-Object { $_.Value.connections -gt 0 } | ForEach-Object { [pscustomobject]@{ id = $_.Name; n = $_.Value.connections } })
  Invoke-Dms setSettings @{ defaultConnections = $Requests } | Out-Null
  foreach ($o in $overrides) { Invoke-Dms setInterface @{ id = $o.id; connections = $Requests } | Out-Null }
  $off = @()
  if ($alone) {
    $off = @((Get-DmsState).interfaces | Where-Object { $_.present -and $_.enabled -and -not $_.vpn -and $_.ip -ne $alone })
    foreach ($i in $off) { Invoke-Dms setInterface @{ id = $i.id; enabled = $false } | Out-Null }
  }
  $add = @{ url = $Url }
  if ($into) { $add.folder = $true }
  elseif ($script:benchDir) { $add.dir = Join-Path $script:benchDir 'dms'; New-Item -ItemType Directory -Force $add.dir | Out-Null }
  $t0 = Get-Date
  Start-Meter
  $r = Invoke-Dms add $add
  # A file of that name already there: DMS² asks first (the onExists setting). Keep both, so the run goes on.
  if ($r.ask) { $r = Invoke-Dms answerAsk @{ id = $r.ask; choice = 'rename' } }
  $jid = $r.id
  if ($into) { Invoke-Dms setFolder @{ id = $jid; on = $true; into = $into } | Out-Null }
  $drive = if ($into) { $into.Substring(0, 1) }
  $free0 = if ($drive) { (Get-PSDrive $drive).Free }
  $minFree = $free0
  $dropped = $null; $dropNote = ''
  try {
    while ($true) {
      Wait-Meter 1
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
        $secs = if ($s -eq 'completed' -and $d.finished) { Get-MeterSeconds (([datetime]$d.finished) - ([datetime]$d.added)).TotalSeconds ([datetime]$d.finished).ToLocalTime() } else { $el }
        $peak = if ($drive) { $free0 - $minFree } else { '' }
        $note = (@($Note, $dropNote, "outcome: $why") | Where-Object { $_ }) -join '; '
        $res = [pscustomobject]@{ Done = ($s -eq 'completed'); Size = $d.size; Seconds = $secs; Links = @($d.links.PSObject.Properties).Count; Peak = $peak; Note = $note }
        # The benchmark's own file and row. deleteFile (to the Recycle Bin) also drops the row;
        # remove deletes only an unfinished download's partial file.
        if ($s -eq 'completed') { Invoke-Dms deleteFile @{ id = $jid } | Out-Null }
        else { Invoke-Dms remove @{ id = $jid; deleteFile = $true } | Out-Null }
        return $res
      }
    }
  } finally {
    # leave the user's lines as they were
    foreach ($i in @($off) + @($dropped | Where-Object { $_ })) { Invoke-Dms setInterface @{ id = $i.id; enabled = $true } | Out-Null }
    Invoke-Dms setSettings @{ defaultConnections = $was.defaultConnections } | Out-Null
    foreach ($o in $overrides) { Invoke-Dms setInterface @{ id = $o.id; connections = $o.n } | Out-Null }
  }
}

# Seconds from $t0 until $path is complete: its full size, and no program writing it (a copy across drives
# sets the size first). A reader, such as a virus scan of the new file, doesn't hold it up. $null after $Timeout.
function Wait-File($path, $size, $t0) {
  while (((Get-Date) - $t0).TotalSeconds -lt $Timeout) {
    Step-Meter
    if ((Test-Path $path) -and (Get-Item $path).Length -eq $size) {
      try { ([IO.File]::Open($path, 'Open', 'Read', 'Read')).Close(); return (Get-MeterSeconds ((Get-Date) - $t0).TotalSeconds) } catch { }
    }
    Start-Sleep -Milliseconds 250
  }
  $script:meter = $null
  $null
}

# The file name the other tools save to, from the link: they all get the same one.
function Get-BenchName { $n = [IO.Path]::GetFileName(([uri]$Url).AbsolutePath); if ($n) { $n } else { 'dms-bench.bin' } }

# One timed aria2 run. Returns the seconds, or $null if the file did not come out whole.
# $lines: how many lines it spreads over, for $Requests on each.
function Invoke-Aria($size, [string[]]$extra, $lines = 1) {
  $n = $Requests * $lines
  $dir = Join-Path $script:benchDir 'aria2'; $name = Get-BenchName
  New-Item -ItemType Directory -Force $dir | Out-Null
  $t0 = Get-Date
  Start-Meter
  $p = Start-Process aria2c -NoNewWindow -PassThru -ArgumentList (@("-x$n", "-s$n", '-k1M', '--file-allocation=none', '--allow-overwrite=true', '-q') + $extra + @('-d', "`"$dir`"", '-o', "`"$name`"", "`"$Url`""))
  while (-not $p.HasExited) { Wait-Meter 0.25 }
  $secs = Get-MeterSeconds ((Get-Date) - $t0).TotalSeconds
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
  $dir = Join-Path $script:benchDir 'idm'; $name = Get-BenchName
  Remove-Item $dir -Recurse -Force -ErrorAction SilentlyContinue
  New-Item -ItemType Directory -Force $dir | Out-Null
  $t0 = Get-Date
  Start-Meter
  if ($IDMViaTask) {
    $cmd = Join-Path $env:TEMP 'dms-bench-idm.cmd'
    Set-Content $cmd -Encoding ASCII -Value "@`"$exe`" /d $Url /p `"$dir`" /f $name /n"
    schtasks /create /tn dms-bench-idm /tr $cmd /sc once /st 23:59 /f | Out-Null
    schtasks /run /tn dms-bench-idm | Out-Null
    Wait-Meter 2; schtasks /delete /tn dms-bench-idm /f | Out-Null # IDM has the link by now
  } else { & $exe /d $Url /p $dir /f $name /n | Out-Null }
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
      "$env:LOCALAPPDATA\JDownloader 2", "$env:LOCALAPPDATA\JDownloader 2.0", "$env:ProgramFiles\JDownloader") | Where-Object { $_ -and (Test-Path (Join-Path $_ 'cfg\org.jdownloader.extensions.folderwatchV2.FolderWatchExtension.json')) } | Select-Object -First 1
  }
  if (-not $jd) { return 'not installed, or never started' }
  # JDownloader writes the folder only once Folder Watch has run; its setting says whether it is on.
  if (-not (Get-Content -Raw (Join-Path $jd 'cfg\org.jdownloader.extensions.folderwatchV2.FolderWatchExtension.json') | ConvertFrom-Json).enabled) { return 'Folder Watch is off (Settings, Extension Modules, Folder Watch)' }
  $chunks = (Get-Content -Raw (Join-Path $jd 'cfg\org.jdownloader.settings.GeneralSettings.json') -ErrorAction SilentlyContinue | ConvertFrom-Json).maxchunksperfile
  if ($chunks -ne $Requests) { return "its max. chunks per download is $chunks, not $Requests (Settings, General)" }
  $watch = Join-Path $jd 'folderwatch'
  New-Item -ItemType Directory -Force $watch | Out-Null
  $dir = Join-Path $script:benchDir 'jd'; $name = Get-BenchName
  Remove-Item $dir -Recurse -Force -ErrorAction SilentlyContinue
  New-Item -ItemType Directory -Force $dir | Out-Null
  $job = Join-Path $watch "dms-bench-$(Get-Date -Format yyyyMMddHHmmss).crawljob"
  $t0 = Get-Date
  Start-Meter
  @("text=$Url", "downloadFolder=$dir", "filename=$name", 'packageName=dms-bench', 'enabled=TRUE',
    'autoConfirm=TRUE', 'autoStart=TRUE', 'forcedStart=TRUE') | Set-Content $job -Encoding ASCII
  while ((Test-Path $job) -and ((Get-Date) - $t0).TotalSeconds -lt 60) { Wait-Meter 0.25 }
  if (Test-Path $job) { Remove-Item $job; return 'JDownloader did not take the job within 60 s: is it running, with Folder Watch on?' }
  $secs = Wait-File "$dir\$name" $size $t0
  Remove-Item $dir -Recurse -Force -ErrorAction SilentlyContinue
  if ($null -eq $secs) { "no whole file after $Timeout s" } else { $secs }
}

# The line Windows sends the server's traffic out of: the one IDM and JDownloader use.
function Get-RouteLine { (Find-NetRoute -RemoteIPAddress $script:serverIP -ErrorAction SilentlyContinue | Where-Object InterfaceAlias | Select-Object -First 1).InterfaceAlias }

# Makes $alias the Windows default line (lowest interface metric, IPv4 and IPv6), for tools that can't
# be bound to a line. Needs an elevated shell. Restore-Lines puts every metric back as it was.
$script:savedMetrics = $null
function Use-Line($alias) {
  if (-not $script:savedMetrics) {
    $script:savedMetrics = @(Get-NetIPInterface -InterfaceAlias $script:nicNames -ErrorAction SilentlyContinue | Select-Object InterfaceAlias, AddressFamily, InterfaceMetric, AutomaticMetric)
  }
  foreach ($m in $script:savedMetrics) {
    Set-NetIPInterface -InterfaceAlias $m.InterfaceAlias -AddressFamily $m.AddressFamily -InterfaceMetric $(if ($m.InterfaceAlias -eq $alias) { 1 } else { 500 })
  }
  Start-Sleep 2
  $now = Get-RouteLine
  if ($now -ne $alias) { Write-Warning "Windows still routes the server over $now, not $alias" }
}
function Restore-Lines {
  foreach ($m in $script:savedMetrics) {
    if ($m.AutomaticMetric -eq 'Enabled') { Set-NetIPInterface -InterfaceAlias $m.InterfaceAlias -AddressFamily $m.AddressFamily -AutomaticMetric Enabled }
    else { Set-NetIPInterface -InterfaceAlias $m.InterfaceAlias -AddressFamily $m.AddressFamily -InterfaceMetric $m.InterfaceMetric }
  }
  $script:savedMetrics = $null
}

# Plexo has no command line. The script starts it with Electron's remote debugging and calls its own
# window's API (window.plexo: probeUrl, startDownload, listHistory), as Plexo's e2e tests do.
$plexoPort = 9339
function Invoke-Cdp($expr) {
  $page = (Invoke-RestMethod "http://127.0.0.1:$plexoPort/json" -TimeoutSec 3) | Where-Object { $_.type -eq 'page' -and $_.url -notlike 'devtools*' } | Select-Object -First 1
  if (-not $page) { throw 'Plexo has no window to talk to.' }
  $ws = New-Object Net.WebSockets.ClientWebSocket
  $none = [Threading.CancellationToken]::None
  try {
    $ws.ConnectAsync([uri]$page.webSocketDebuggerUrl, $none).Wait()
    $msg = @{ id = 1; method = 'Runtime.evaluate'; params = @{ expression = $expr; awaitPromise = $true; returnByValue = $true } } | ConvertTo-Json -Compress -Depth 5
    $bytes = [Text.Encoding]::UTF8.GetBytes($msg)
    $ws.SendAsync((New-Object 'ArraySegment[byte]' (, $bytes)), 'Text', $true, $none).Wait()
    $buf = New-Object byte[] 65536; $text = New-Object Text.StringBuilder
    while ($true) {
      $r = $ws.ReceiveAsync((New-Object 'ArraySegment[byte]' (, $buf)), $none).Result
      [void]$text.Append([Text.Encoding]::UTF8.GetString($buf, 0, $r.Count))
      if (-not $r.EndOfMessage) { continue }
      $j = $text.ToString() | ConvertFrom-Json
      if ($j.id -eq 1) { break }
      [void]$text.Clear()
    }
  } finally { $ws.Dispose() }
  if ($j.result.exceptionDetails) { throw "Plexo: $($j.result.exceptionDetails.exception.description)" }
  $j.result.result.value
}
# Starts Plexo with the port; returns why not, or $null once its window answers.
function Start-Plexo {
  $exe = "$env:LOCALAPPDATA\Programs\plexo\plexo.exe"
  if (-not (Test-Path $exe)) {
    $cmd = (Get-ItemProperty 'HKCU:\Software\Classes\plexo\shell\open\command' -ErrorAction SilentlyContinue).'(default)'
    $exe = if ($cmd -match '^"([^"]+)"') { $Matches[1] }
    if (-not $exe -or -not (Test-Path $exe)) { return 'not installed' }
  }
  if (Get-Process plexo -ErrorAction SilentlyContinue) { return 'running already: quit it (tray icon, Quit) so the script can start it with remote debugging' }
  $script:plexoStarted = $true
  Start-Process $exe "--remote-debugging-port=$plexoPort" | Out-Null
  $t0 = Get-Date
  while (((Get-Date) - $t0).TotalSeconds -lt 60) {
    Start-Sleep 1
    try { if (Invoke-Cdp 'typeof window.plexo === "object"') { return $null } } catch { }
  }
  'did not answer on its debugging port within 60 s'
}
function Stop-Plexo { if ($script:plexoStarted) { Get-Process plexo -ErrorAction SilentlyContinue | Stop-Process -Force; $script:plexoStarted = $false } }
# One Plexo run over the lines named (Windows adapter names, which Plexo uses as network ids).
# Returns the seconds, or why not.
function Invoke-Plexo($size, [string[]]$aliases) {
  $dir = Join-Path $script:benchDir 'plexo'; $name = Get-BenchName
  Remove-Item $dir -Recurse -Force -ErrorAction SilentlyContinue
  $a = @{ url = $Url; dir = $dir; name = $name; ifs = $aliases; streams = $Requests } | ConvertTo-Json -Compress
  $t0 = Get-Date
  Start-Meter
  $id = Invoke-Cdp (@'
(async () => {
  const a = ARGS
  const ids = (await plexo.listInterfaces()).map((i) => i.id)
  const missing = a.ifs.filter((i) => !ids.includes(i))
  if (missing.length) return 'Plexo has no network ' + missing.join(', ')
  const p = await plexo.probeUrl(a.url)
  const ranged = p.supportsRanges && p.totalBytes !== null
  return 'id:' + await plexo.startDownload({ kind: 'http', url: p.finalUrl, destinationDir: a.dir, suggestedFileName: a.name,
    totalBytes: p.totalBytes ?? 0, supportsRanges: ranged, interfaceIds: ranged ? a.ifs : a.ifs.slice(0, 1), etag: p.etag, lastModified: p.lastModified, streamsPerNetwork: a.streams })
})()
'@.Replace('ARGS', $a))
  if ($id -notlike 'id:*') { $script:meter = $null; return $id }
  $id = $id.Substring(3)
  $status = $null
  while (((Get-Date) - $t0).TotalSeconds -lt $Timeout) {
    Wait-Meter 0.25
    $status = Invoke-Cdp "(async () => (await plexo.listHistory()).find((e) => e.id === '$id')?.status ?? null)()"
    if ($status) { break }
  }
  $ok = $status -eq 'completed' -and (Test-Path "$dir\$name") -and (Get-Item "$dir\$name").Length -eq $size
  $secs = if ($ok) { Get-MeterSeconds ((Get-Date) - $t0).TotalSeconds } else { $script:meter = $null }
  if (-not $status) { Invoke-Cdp "plexo.cancelDownload('$id')" | Out-Null }
  Remove-Item $dir -Recurse -Force -ErrorAction SilentlyContinue
  if ($ok) { $secs } elseif ($status) { "ended $status" } else { "not finished after $Timeout s" }
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
        # per line ("Windows default" in rows from before every line was run)
        $g | Where-Object { $conventional -contains $_.tool } | Group-Object lines | ForEach-Object {
          $conv = @($_.Group | Group-Object tool | ForEach-Object { & $avg $_.Group })
          $out["a conventional download manager: $($_.Name)"] = "$([math]::Round(($conv | Measure-Object -Average).Average, 1)) ($($conv.Count) tools)"
        }
        $d = & $avg ($g | Where-Object { $_.tool -eq 'DMS²' -and $_.lines -like '*connections' }); if ($null -ne $d) { $out['DMS²'] = $d }
        $g | Where-Object { $_.tool -eq 'DMS²' -and $_.lines -notlike '*connections' } | Group-Object lines | ForEach-Object { $out["DMS²: $($_.Name) alone"] = & $avg $_.Group }
        if ($own) { $g | Where-Object { $_.tool -ne 'one request (curl)' -and $_.tool -ne 'DMS²' } | Group-Object tool, lines | ForEach-Object { $out["$($_.Group[0].tool): $($_.Group[0].lines)"] = & $avg $_.Group } }
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

# Windows sends a socket bound to an address out of that address's interface, so this is one line alone.
$lines = Get-NetIPConfiguration | Where-Object { $_.IPv4DefaultGateway -and $_.NetAdapter.Status -eq 'Up' }
# The lines DMS² downloads over, when it runs: a system VPN (it rides on one of them) is not a line of its own.
try {
  Connect-Dms
  if (-not $Dir) { $Dir = Join-Path (Invoke-Dms getSettings).saveDir 'dms-bench' }
  $dmsIPs = @((Get-DmsState).interfaces | Where-Object { $_.enabled -and $_.present -and -not $_.vpn -and -not $_.tunnel } | ForEach-Object { $_.ip })
  if ($dmsIPs.Count) { $lines = @($lines | Where-Object { $dmsIPs -contains @($_.IPv4Address)[0].IPAddress }) }
  $script:wr.Close() # an idle connection fills with the engine's events and is dropped: the DMS² step opens its own
} catch { Write-Host "DMS² not reachable: every connection with a gateway counts as a line" }
$script:nicNames = @($lines | ForEach-Object { $_.InterfaceAlias })
if (-not $Dir) { $Dir = Join-Path $env:TEMP 'dms-bench' }
$script:benchDir = $Dir
New-Item -ItemType Directory -Force $Dir | Out-Null
Write-Host "Every tool writes into $Dir"

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

# One request over one line (curl bound to its address).
function Invoke-Curl($l, $ip) {
  Write-Host "One connection alone: $($l.InterfaceAlias) ($ip)"
  $wout = Join-Path $env:TEMP 'dms-bench-curl.txt'
  Start-Meter
  $p = Start-Process curl.exe -NoNewWindow -PassThru -RedirectStandardOutput $wout -ArgumentList '--interface', $ip, '-sL', '-o', 'NUL', '-w', '"%{size_download} %{time_total}"', "`"$Url`""
  while (-not $p.HasExited) { Wait-Meter 0.25 }
  $got, $secs = (Get-Content $wout -Raw) -split ' '
  Remove-Item $wout -ErrorAction SilentlyContinue
  $secs = Get-MeterSeconds ([double]$secs)
  if ([long]$got -eq $size) { Save 'speed' 'one request (curl)' $l.InterfaceAlias $size $secs '' }
  else { Write-Warning "$($l.InterfaceAlias): got $got of $size bytes, not saved" }
}
function Want($step) { -not $Only -or $Only -eq $step }
$aria = Get-Command aria2c -ErrorAction SilentlyContinue
$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
$script:serverIP = (Resolve-DnsName ([uri]$Url).Host -Type A -ErrorAction SilentlyContinue | Where-Object IPAddress | Select-Object -First 1).IPAddress
$defaultLine = Get-RouteLine
if (-not $NoOthers -and -not $aria -and (Want 'aria2')) { Write-Host 'aria2: not installed (aria2c not on PATH), skipped' }
if (-not $NoOthers -and -not $isAdmin -and ((Want 'IDM') -or (Want 'JDownloader'))) {
  Write-Host "Not elevated: IDM and JDownloader run only on the Windows default line ($defaultLine). Run elevated to time them on every line."
}
# Each line alone, with each tool that downloads over one line: curl and aria2 bound to its address,
# IDM and JDownloader with that line made the Windows default (they can't be bound).
try {
foreach ($l in $lines) {
  $alias = $l.InterfaceAlias
  $ip = @($l.IPv4Address)[0].IPAddress
  if (Want 'lines') { Invoke-Curl $l $ip }
  if ($NoOthers) { continue }
  if ((Want 'aria2') -and $aria) {
    Write-Host "aria2 over $alias"
    $secs = Invoke-Aria $size @("--interface=$ip")
    if ($secs) { Save 'speed' 'aria2' $alias $size $secs '' } else { Write-Warning 'aria2 did not finish the file, not saved' }
  }
  if (-not ((Want 'IDM') -or (Want 'JDownloader'))) { continue }
  if ($isAdmin) { Use-Line $alias } elseif ($alias -ne $defaultLine) { continue }
  if (Want 'IDM') {
    Write-Host "IDM over $alias"
    $r = Invoke-IDM $size
    if ($r -is [double]) { Save 'speed' 'IDM' $alias $size $r '' } else { Write-Host "  IDM: $r, skipped" }
  }
  if (Want 'JDownloader') {
    Write-Host "JDownloader over $alias"
    $r = Invoke-JDownloader $size
    if ($r -is [double]) { Save 'speed' 'JDownloader' $alias $size $r '' } else { Write-Host "  JDownloader: $r, skipped" }
  }
}
} finally { Restore-Lines }

if ($Personal -and ((Want 'aria2') -or (Want 'Plexo'))) {
  # Other tools that use every connection: for a personal comparison, kept out of results.csv.
  $ips = @($lines | ForEach-Object { @($_.IPv4Address)[0].IPAddress })
  $all = @($lines.InterfaceAlias) -join ' + '
  if (-not (Want 'aria2')) {
  } elseif ($aria -and $ips.Count -ge 2) {
    Write-Host "aria2 over every line ($($ips -join ', '))"
    $secs = Invoke-Aria $size @("--multiple-interface=$($ips -join ',')") $ips.Count
    if ($secs) { Save 'speed' 'aria2, every line' $all $size $secs '' $personalCsv }
    else { Write-Warning 'aria2 over every line did not finish the file, not saved' }
  } else { Write-Host 'aria2 over every line: needs aria2 and two lines up, skipped' }
  if (Want 'Plexo') {
    $why = Start-Plexo
    if ($why) { Write-Host "Plexo: $why, skipped" }
    else {
      try {
        # every line, then each line alone
        $sets = @(, @($lines.InterfaceAlias))
        if ($lines.Count -ge 2) { foreach ($x in $lines.InterfaceAlias) { $sets += , @($x) } }
        foreach ($set in $sets) {
          $label = $set -join ' + '
          Write-Host "Plexo over $label"
          $r = Invoke-Plexo $size $set
          if ($r -is [double]) { Save 'speed' 'Plexo' $label $size $r '' $personalCsv } else { Write-Host "  Plexo: $r, skipped" }
        }
      } finally { Stop-Plexo }
    }
  }
}

if ($Only -and $Only -ne 'DMS') { return }
if ($Unattended) {
  $r = Invoke-DmsRun $null $null
  if ($r.Done) { Save 'speed' 'DMS²' "$($r.Links) connections" $r.Size $r.Seconds '' }
  else { Write-Warning "Not finished: $($r.Note)" }
  # and each line alone: the others switched off in DMS² for the run
  if ($lines.Count -ge 2) {
    foreach ($l in $lines) {
      Write-Host "DMS² over $($l.InterfaceAlias) alone"
      $r = Invoke-DmsRun $null $null @($l.IPv4Address)[0].IPAddress
      if ($r.Done) { Save 'speed' 'DMS²' $l.InterfaceAlias $r.Size $r.Seconds '' }
      else { Write-Warning "Not finished: $($r.Note)" }
    }
  }
  return
}
Write-Host 'DMS² on each line alone needs -Unattended, skipped'

$since = Get-Date
Write-Host "`nNow DMS²: make sure nothing else is downloading, then + Add this link:`n  $Url"
Write-Host 'Press Enter when DMS² shows it as finished'
Start-Meter
while (-not ([Console]::KeyAvailable -and [Console]::ReadKey($true).Key -eq 'Enter')) { Wait-Meter 0.25 }
$run = Get-DmsRun $Url $since
if ($run) { Save 'speed' 'DMS²' "$($run.Links) connections" $run.Size (Get-MeterSeconds $run.Seconds $run.Finished) '' }
else { Write-Warning "No finished DMS² download of that link since $since. Nothing saved for DMS²." }
