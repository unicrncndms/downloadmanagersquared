# Writes dist\THIRD-PARTY-NOTICES: every licence linked into the engine, then
# Go's own (the standard library and runtime are in every exe) and the PIA CA.
$ErrorActionPreference = 'Stop'
$env:CGO_ENABLED = '0' # the modules a pure-Go build links, as build.ps1 builds
$root = Split-Path (Split-Path $PSScriptRoot -Parent) -Parent
$out = Join-Path $root 'dist\THIRD-PARTY-NOTICES'
New-Item -ItemType Directory -Force (Split-Path $out) | Out-Null

$tpl = Join-Path $root '.github\third-party-notices.tpl'
Push-Location (Join-Path $root 'engine')
try {
    # Built here, with the Go that engine/go.mod asks for: built by an older
    # installed Go, it takes the standard library for modules without info.
    go install github.com/google/go-licenses@v1.6.0
    if ($LASTEXITCODE) { throw 'installing go-licenses failed' }
    $report = go-licenses report ./... --ignore downloadmanagersquared/engine --template $tpl
    if ($LASTEXITCODE) { throw 'go-licenses report failed' }
} finally {
    Pop-Location
}
$goVersion = (go env GOVERSION)
$goLicense = Get-Content -Raw (Join-Path (go env GOROOT) 'LICENSE')
$piaLicense = Get-Content -Raw (Join-Path $root 'engine\internal\pia\LICENSE.manual-connections')
$fontLicense = Get-Content -Raw (Join-Path $root 'extension\ui\fonts\OFL.txt')
$sep = "`n================================================================================`n"
$text = ($report -join "`n") +
    $sep + "The Go standard library and runtime ($goVersion)`nLicense: BSD-3-Clause`n`n" + $goLicense +
    $sep + "ca.rsa.4096.crt from github.com/pia-foss/manual-connections (embedded in the engine)`nLicense: MIT`n`n" + $piaLicense +
    $sep + "Rubik, the pages' font (ui\fonts\Rubik.ttf, from github.com/google/fonts)`nLicense: OFL-1.1`n`n" + $fontLicense
[IO.File]::WriteAllText($out, $text, (New-Object Text.UTF8Encoding $false))
$mpl = ($report | Select-String '^License: MPL-2.0').Count
Write-Host "Wrote $out ($mpl MPL-2.0 modules)"
