# Writes the release notes for -Tag: the install sentence, the changes since the
# previous v* tag (commit subjects are written as user-facing changes), and,
# once the files are final, the SHA-256 of each attached file.
param(
    [Parameter(Mandatory)][string]$Tag,
    [string[]]$Files = @()
)
$ErrorActionPreference = 'Stop'

$lines = @(
    'Download downloadmanagersquared-windows.zip, extract it and run dms-engine.exe. Setup then adds the extension to your browser.'
    ''
)

$previous = git tag --merged "$Tag^" --list 'v*' --sort=-v:refname | Select-Object -First 1
if ($LASTEXITCODE) { throw "reading the tags before $Tag failed" }
$range = if ($previous) { "$previous..$Tag" } else { $Tag }
$subjects = @(git log --no-merges --format=%s $range)
if ($LASTEXITCODE) { throw "reading the commits for $range failed" }

$lines += if ($previous) { "## Changes since $previous" } else { '## Changes' }
$lines += ''
$lines += if ($subjects.Count) { $subjects | ForEach-Object { "- $_" } } else { '- No changes.' }

if ($Files.Count) {
    $lines += ''
    $lines += '## SHA-256'
    $lines += ''
    foreach ($f in $Files) {
        $hash = (Get-FileHash -Algorithm SHA256 $f).Hash.ToLower()
        $lines += "- ``$(Split-Path -Leaf $f)``: ``$hash``"
    }
}

$lines -join "`n"
