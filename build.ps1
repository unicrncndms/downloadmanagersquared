# Builds the engine, the native-messaging shim and the extension package into dist\,
# all stamped with one version: -Version, else the latest git tag, else 0.0.0.
# -Repo owner/repo is where the engine sends people for the extension and releases.
param([string]$Version, [string]$Repo)
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$dist = Join-Path $root 'dist'
New-Item -ItemType Directory -Force $dist | Out-Null

if (-not $Version) {
    $tag = git -C $root describe --tags --abbrev=0 2>$null
    $Version = if ($LASTEXITCODE -eq 0 -and $tag) { $tag } else { '0.0.0' }
}
$Version = $Version.TrimStart('v')
if ($Version -notmatch '^\d+\.\d+\.\d+$') { throw "version '$Version' is not X.Y.Z" }

$go = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $go) { throw 'Go is not on PATH. Install it (winget install GoLang.Go) and open a new terminal.' }

$ldflags = "-X downloadmanagersquared/engine/internal/engine.Version=$Version"
if ($Repo) { $ldflags += " -X downloadmanagersquared/engine/internal/install.Repo=$Repo" }
Push-Location (Join-Path $root 'engine')
try {
    & $go test ./...
    if ($LASTEXITCODE) { throw 'tests failed' }
    & $go build -trimpath -ldflags "$ldflags" -o "$dist\dms-host.exe" ./cmd/dms-host
    if ($LASTEXITCODE) { throw 'building dms-host failed' }
    & $go build -trimpath -ldflags "-H windowsgui -s -w $ldflags" -o "$dist\dms-engine.exe" ./cmd/dms-engine
    if ($LASTEXITCODE) { throw 'building dms-engine failed' }
} finally {
    Pop-Location
}

if (Get-Command node -ErrorAction SilentlyContinue) {
    node --test (Join-Path $root 'extension\test')
    if ($LASTEXITCODE) { throw 'extension tests failed' }
} else {
    Write-Host 'node is not on PATH: extension tests skipped'
}

# The extension is packed from a staged copy whose manifest carries the version;
# the repo's manifest stays at 0.0.0.
$stage = Join-Path $dist 'extension'
Remove-Item -Recurse -Force $stage -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force $stage | Out-Null
$parts = 'lib', 'background', 'content', 'ui', 'icons', 'resolver-content.js'
foreach ($p in $parts) { Copy-Item -Recurse (Join-Path $root "extension\$p") $stage }
$manifest = Get-Content -Raw -Encoding UTF8 (Join-Path $root 'extension\manifest.json')
$manifest = $manifest -replace '"version":\s*"[^"]*"', "`"version`": `"$Version`""
if ($Repo) {
    # Firefox updates a self-hosted extension from the latest release's updates.json.
    $manifest = $manifest -replace '("id":\s*"downloadmanagersquared@dms",)', "`$1`n      `"update_url`": `"https://github.com/$Repo/releases/latest/download/updates.json`","
}
[IO.File]::WriteAllText((Join-Path $stage 'manifest.json'), $manifest, (New-Object Text.UTF8Encoding $false))

# Zip with forward slashes (Compress-Archive writes backslashes, which AMO rejects).
$xpi = Join-Path $dist 'downloadmanagersquared.xpi'
Remove-Item $xpi -ErrorAction SilentlyContinue
Push-Location $stage
try {
    tar.exe -a -c -f $xpi manifest.json @parts
    if ($LASTEXITCODE) { throw 'packing the extension failed' }
} finally {
    Pop-Location
}

# The release zip: the two exes and what goes with them.
$zipStage = Join-Path $dist 'zip'
Remove-Item -Recurse -Force $zipStage -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force $zipStage | Out-Null
Copy-Item "$dist\dms-engine.exe", "$dist\dms-host.exe", (Join-Path $root 'LICENSE') $zipStage
Copy-Item (Join-Path $root 'installer\README.txt') $zipStage
if (Test-Path "$dist\THIRD-PARTY-NOTICES") { Copy-Item "$dist\THIRD-PARTY-NOTICES" $zipStage }
$zip = Join-Path $dist 'downloadmanagersquared-windows.zip'
Remove-Item $zip -ErrorAction SilentlyContinue
Push-Location $zipStage
try {
    tar.exe -a -c -f $zip (Get-ChildItem -Name)
    if ($LASTEXITCODE) { throw 'packing the release zip failed' }
} finally {
    Pop-Location
}
Write-Host "Built $Version into $dist"
