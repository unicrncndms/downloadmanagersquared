# Writes dist\THIRD-PARTY-NOTICES: every licence linked into the engine, then
# Go's own (the standard library and runtime are in every exe).
$ErrorActionPreference = 'Stop'
$root = Split-Path (Split-Path $PSScriptRoot -Parent) -Parent
$out = Join-Path $root 'dist\THIRD-PARTY-NOTICES'
New-Item -ItemType Directory -Force (Split-Path $out) | Out-Null

go install github.com/google/go-licenses@v1.6.0
if ($LASTEXITCODE) { throw 'installing go-licenses failed' }
$tpl = Join-Path $root '.github\third-party-notices.tpl'
Push-Location (Join-Path $root 'engine')
try {
    $report = go-licenses report ./... --ignore downloadmanagersquared/engine --template $tpl
    if ($LASTEXITCODE) { throw 'go-licenses report failed' }
} finally {
    Pop-Location
}
$goVersion = (go env GOVERSION)
$goLicense = Get-Content -Raw (Join-Path (go env GOROOT) 'LICENSE')
$text = ($report -join "`n") + "`n================================================================================`nThe Go standard library and runtime ($goVersion)`nLicense: BSD-3-Clause`n`n" + $goLicense
[IO.File]::WriteAllText($out, $text, (New-Object Text.UTF8Encoding $false))
$mpl = ($report | Select-String '^License: MPL-2.0').Count
Write-Host "Wrote $out ($mpl MPL-2.0 modules)"
