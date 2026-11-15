# Runs only the tests for what changed since -Base (default HEAD), untracked files included.
# -List prints the commands it would run and runs none. Exits non-zero if a test run fails.
param([string]$Base = 'HEAD', [switch]$List)
$root = $PSScriptRoot
Push-Location $root

$files = @(git diff --name-only $Base)
if ($LASTEXITCODE) { throw "git diff against '$Base' failed" }
$files = @(($files + @(git ls-files --others --exclude-standard)) | Where-Object { $_ } | Sort-Object -Unique)

$runAll = $false; $runNode = $false; $goFiles = @()
foreach ($f in $files) {
    if ($f -match '^(build\.ps1|\.github/|engine/go\.(mod|sum)$)') { $runAll = $true }
    elseif ($f -match '^extension/' -and $f -notmatch '^extension/dev/') { $runNode = $true }
    elseif ($f -match '^engine/.*\.go$') { $goFiles += $f }
    if ($f -eq 'engine/internal/engine/api.go') { $runNode = $true }  # protocol.test.js checks it
}
if ($runAll) { $runNode = $true }

$goPkgs = @()
if ($runAll) { $goPkgs = @('./...') }
elseif ($goFiles.Count) {
    # Each package with its dependencies and test imports; the changed ones and their importers are tested.
    Push-Location (Join-Path $root 'engine')
    $lines = @(go list -f '{{.ImportPath}}|{{.Dir}}|{{range .Deps}}{{.}},{{end}}{{range .TestImports}}{{.}},{{end}}{{range .XTestImports}}{{.}},{{end}}' ./...)
    Pop-Location
    $pkgs = @($lines | ForEach-Object { $p = $_.Split('|'); [pscustomobject]@{ Path = $p[0]; Dir = $p[1]; Deps = @($p[2].Split(',') | Where-Object { $_ }) } })
    $dirs = @($goFiles | ForEach-Object { Split-Path (Join-Path $root ($_ -replace '/', '\')) -Parent })
    $changed = @($pkgs | Where-Object { $dirs -contains $_.Dir } | ForEach-Object { $_.Path })
    $goPkgs = @($pkgs | Where-Object { ($changed -contains $_.Path) -or @($_.Deps | Where-Object { $changed -contains $_ }).Count } | ForEach-Object { $_.Path })
    Write-Host "changed packages: $($changed -join ' ')"
}

if (-not $goPkgs.Count -and -not $runNode) { Write-Host 'nothing to test'; Pop-Location; exit 0 }

$failed = $false
if ($goPkgs.Count) {
    $goArgs = @('test') + $goPkgs
    Write-Host "> cd engine; go $($goArgs -join ' ')"
    if (-not $List) {
        Push-Location (Join-Path $root 'engine')
        & go @goArgs
        if ($LASTEXITCODE) { $failed = $true }
        Pop-Location
    }
}
if ($runNode) {
    Write-Host '> node --test "extension/test/*.test.js"'
    if (-not $List) {
        & node --test 'extension/test/*.test.js'
        if ($LASTEXITCODE) { $failed = $true }
    }
}
Pop-Location
if ($failed) { Write-Host 'tests FAILED'; exit 1 }
exit 0
