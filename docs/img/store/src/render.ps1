# Regenerates the store screenshots (docs/img/store/*.png) from the UI harness fixture "store".
# Needs Python and Edge. Run from anywhere: powershell -ExecutionPolicy Bypass -File docs/img/store/src/render.ps1
$ErrorActionPreference = "Stop"
$root = (Resolve-Path "$PSScriptRoot\..\..\..\..").Path
$out = Join-Path $root "docs\img\store"
$port = 8791
$edge = (Get-ChildItem "C:\Program Files (x86)\Microsoft\EdgeCore\*\msedge.exe", "C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe", "C:\Program Files\Microsoft\Edge\Application\msedge.exe" -ErrorAction SilentlyContinue | Select-Object -First 1).FullName
if (-not $edge) { throw "msedge.exe not found" }
$shots = [ordered]@{ speed = "01-speed"; map = "02-map"; folder = "03-folder"; connections = "04-connections" }
$server = Start-Process python -ArgumentList "`"$root\extension\dev\serve.py`"", $port -PassThru -WindowStyle Hidden
try {
  Start-Sleep 2
  foreach ($s in $shots.Keys) {
    & $edge --headless=new --screenshot="$out\$($shots[$s]).png" --window-size=1280,800 --hide-scrollbars --force-device-scale-factor=1 --virtual-time-budget=4000 "http://127.0.0.1:$port/dev/store/shot.html?s=$s" | Out-Null
  }
} finally { Stop-Process -Id $server.Id -Force }
