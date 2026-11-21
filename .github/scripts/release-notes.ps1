# Writes the release notes for -Tag: the install sentence, docs/releases/<tag>.md
# (written for users before tagging, see CONTRIBUTING), and, once the files are
# final, the SHA-256 of each attached file in a collapsed block at the end.
# Throws when the notes file is missing: no release goes out as commit subjects.
param(
    [Parameter(Mandatory)][string]$Tag,
    [string[]]$Files = @()
)
$ErrorActionPreference = 'Stop'

$curated = Join-Path $PSScriptRoot "..\..\docs\releases\$Tag.md"
if (-not (Test-Path $curated)) {
    throw "docs/releases/$Tag.md is missing. Write the release notes (format in CONTRIBUTING.md, Releasing), commit them, then tag again with the next version."
}

$lines = @(
    'Download downloadmanagersquared-windows.zip, extract it and run dms-engine.exe. Setup then adds the extension to your browser.'
    ''
    (Get-Content -Raw -Encoding utf8 $curated).TrimEnd()
)

if ($Files.Count) {
    $lines += ''
    $lines += '<details><summary>SHA-256</summary>'
    $lines += ''
    foreach ($f in $Files) {
        $hash = (Get-FileHash -Algorithm SHA256 $f).Hash.ToLower()
        $lines += "- ``$(Split-Path -Leaf $f)``: ``$hash``"
    }
    $lines += ''
    $lines += '</details>'
}

$lines -join "`n"
