# Builds the engine, the native-messaging shim and the extension package into dist\.
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$dist = Join-Path $root 'dist'
New-Item -ItemType Directory -Force $dist | Out-Null

$go = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $go) { $go = 'C:\Program Files\Go\bin\go.exe' }

Push-Location (Join-Path $root 'engine')
try {
    & $go test ./internal/download/
    if ($LASTEXITCODE) { throw 'tests failed' }
    & $go build -trimpath -o "$dist\dms-host.exe" ./cmd/dms-host
    if ($LASTEXITCODE) { throw 'building dms-host failed' }
    & $go build -trimpath -ldflags '-H windowsgui -s -w' -o "$dist\dms-engine.exe" ./cmd/dms-engine
    if ($LASTEXITCODE) { throw 'building dms-engine failed' }
} finally {
    Pop-Location
}

# Zip with forward slashes (Compress-Archive writes backslashes, which AMO rejects).
$xpi = Join-Path $dist 'downloadmanagersquared.zip'
Remove-Item $xpi -ErrorAction SilentlyContinue
Push-Location (Join-Path $root 'extension')
try {
    tar.exe -a -c -f $xpi manifest.json background ui icons resolver-content.js
    if ($LASTEXITCODE) { throw 'packing the extension failed' }
} finally {
    Pop-Location
}
Write-Host "Built into $dist"
