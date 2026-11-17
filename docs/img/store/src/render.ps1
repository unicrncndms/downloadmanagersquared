# Regenerates the store screenshots from the UI harness fixture "store":
# docs/img/store/*.png (1280x800, Chrome Web Store) and docs/img/store/hd/*.png (2560x1600, AMO).
# Needs Python and Edge. Run from anywhere: powershell -ExecutionPolicy Bypass -File docs/img/store/src/render.ps1
$ErrorActionPreference = "Stop"
$root = (Resolve-Path "$PSScriptRoot\..\..\..\..").Path
$out = Join-Path $root "docs\img\store"
New-Item -ItemType Directory -Force "$out\hd" | Out-Null
$port = 8791
$edge = (Get-ChildItem "C:\Program Files (x86)\Microsoft\EdgeCore\*\msedge.exe", "C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe", "C:\Program Files\Microsoft\Edge\Application\msedge.exe" -ErrorAction SilentlyContinue | Select-Object -First 1).FullName
if (-not $edge) { throw "msedge.exe not found" }
$shots = [ordered]@{ speed = "01-speed"; popup = "02-toolbar"; map = "03-map"; folder = "04-folder"; connections = "05-connections" }
$server = Start-Process python -ArgumentList "`"$root\extension\dev\serve.py`"", $port -PassThru -WindowStyle Hidden
try {
  Start-Sleep 2
  foreach ($s in $shots.Keys) {
    foreach ($set in @(@{ dir = $out; scale = 1 }, @{ dir = "$out\hd"; scale = 2 })) {
      & $edge --headless=new --screenshot="$($set.dir)\$($shots[$s]).png" --window-size=1280,800 --hide-scrollbars --force-device-scale-factor=$($set.scale) --virtual-time-budget=4000 "http://127.0.0.1:$port/dev/store/shot.html?s=$s" | Out-Null
    }
  }
} finally { Stop-Process -Id $server.Id -Force }
