# Builds the engine, the native-messaging shim and the extension package into dist\,
# all stamped with one version: -Version, else the latest git tag, else 0.0.0.
# -Repo owner/repo is where the engine sends people for the extension and releases.
param([string]$Version, [string]$Repo)
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$dist = Join-Path $root 'dist'
New-Item -ItemType Directory -Force $dist | Out-Null

if (-not $Version) {
    # Not `git describe`: with no tag it writes to stderr, which Windows
    # PowerShell 5.1 turns into a terminating error under 'Stop'.
    $tag = git -C $root tag --list 'v*' --merged HEAD --sort=-version:refname | Select-Object -First 1
    $Version = if ($tag) { $tag } else { '0.0.0' }
}
$Version = $Version.TrimStart('v')
if ($Version -notmatch '^\d+\.\d+\.\d+$') { throw "version '$Version' is not X.Y.Z" }

$go = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $go) { throw 'Go is not on PATH. Install it (winget install GoLang.Go) and open a new terminal.' }

# Pure Go, as the licence notices assume: with a C compiler on PATH (GitHub's
# runners have one) cgo would link sqlite modules the notices don't list.
$env:CGO_ENABLED = '0'
$ldflags = "-X downloadmanagersquared/engine/internal/engine.Version=$Version"
if ($Repo) { $ldflags += " -X downloadmanagersquared/engine/internal/install.Repo=$Repo" }
# Every zip carries the licences of what is linked in (MIT/BSD/MPL require it).
& (Join-Path $root '.github\scripts\notices.ps1')

# dms-engine.exe carries the host and the notices (internal\bundle), so it
# installs on its own, e.g. double-clicked inside the zip, where Explorer
# extracts only that exe. It also carries the extension's pages for its
# Downloads window (internal\ui), in their own layout: ui\..., lib\version.js
# and icons\icon.svg, which the pages load as ../lib/version.js and
# ../icons/icon.svg. Both folders are emptied again afterwards.
$bundle = Join-Path $root 'engine\internal\bundle\files'
$pages = Join-Path $root 'engine\internal\ui\files'
Push-Location (Join-Path $root 'engine')
try {
    & $go test ./...
    if ($LASTEXITCODE) { throw 'tests failed' }
    & $go build -trimpath -ldflags "$ldflags" -o "$dist\dms-host.exe" ./cmd/dms-host
    if ($LASTEXITCODE) { throw 'building dms-host failed' }
    Copy-Item "$dist\dms-host.exe", (Join-Path $root 'LICENSE'), "$dist\THIRD-PARTY-NOTICES" $bundle
    Copy-Item (Join-Path $root 'extension\ui') $pages -Recurse
    New-Item -ItemType Directory -Force (Join-Path $pages 'lib') | Out-Null
    Copy-Item (Join-Path $root 'extension\lib\version.js') (Join-Path $pages 'lib')
    New-Item -ItemType Directory -Force (Join-Path $pages 'icons') | Out-Null
    Copy-Item (Join-Path $root 'extension\icons\icon.svg') (Join-Path $pages 'icons')
    & $go build -trimpath -ldflags "-H windowsgui -s -w $ldflags" -o "$dist\dms-engine.exe" ./cmd/dms-engine
    if ($LASTEXITCODE) { throw 'building dms-engine failed' }
} finally {
    Get-ChildItem $bundle -Exclude '.gitkeep' | Remove-Item -Force
    Get-ChildItem $pages -Exclude '.gitkeep' | Remove-Item -Recurse -Force
    Pop-Location
}

if (Get-Command node -ErrorAction SilentlyContinue) {
    node --test (Join-Path $root 'extension\test\*.test.js') # a glob: Node 24 doesn't take a folder
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
if ($Repo) {
    $common = Join-Path $stage 'ui\common.js'
    $js = (Get-Content -Raw -Encoding UTF8 $common) -replace 'github\.com/[^/"]+/[^/"]+/releases', "github.com/$Repo/releases"
    [IO.File]::WriteAllText($common, $js, (New-Object Text.UTF8Encoding $false))
}

# Zip with forward slashes (Compress-Archive writes backslashes, which AMO rejects).
# Windows' own bsdtar: Git's GNU tar, often earlier on PATH, can't write zips.
$tar = Join-Path $env:SystemRoot 'System32\tar.exe'
$xpi = Join-Path $dist 'downloadmanagersquared.xpi'
Remove-Item $xpi -ErrorAction SilentlyContinue
Push-Location $stage
try {
    # -a picks the format from the extension and doesn't know .xpi (it writes a tar).
    & $tar --format zip -c -f $xpi manifest.json @parts
    if ($LASTEXITCODE) { throw 'packing the extension failed' }
} finally {
    Pop-Location
}
$magic = [IO.File]::ReadAllBytes($xpi)[0..1]
if ($magic[0] -ne 0x50 -or $magic[1] -ne 0x4B) { throw "$xpi is not a zip" }

# The release zip: the two exes and what goes with them.
$zipStage = Join-Path $dist 'zip'
Remove-Item -Recurse -Force $zipStage -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force $zipStage | Out-Null
Copy-Item "$dist\dms-engine.exe", "$dist\dms-host.exe", (Join-Path $root 'LICENSE') $zipStage
Copy-Item (Join-Path $root 'installer\README.txt') $zipStage
if (-not (Test-Path "$dist\THIRD-PARTY-NOTICES")) { throw 'THIRD-PARTY-NOTICES is missing: no zip without it' }
Copy-Item "$dist\THIRD-PARTY-NOTICES" $zipStage
$zip = Join-Path $dist 'downloadmanagersquared-windows.zip'
Remove-Item $zip -ErrorAction SilentlyContinue
Push-Location $zipStage
try {
    & $tar -a -c -f $zip (Get-ChildItem -Name)
    if ($LASTEXITCODE) { throw 'packing the release zip failed' }
} finally {
    Pop-Location
}
Write-Host "Built $Version into $dist"
