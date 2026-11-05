# Attaches the signed extension and its updates.json to the draft release of
# -Tag, then publishes it. A release is only ever made public here, with its .xpi.
param(
    [Parameter(Mandatory)][string]$Tag,
    [Parameter(Mandatory)][string]$Xpi
)
$ErrorActionPreference = 'Stop'
$repo = $env:GITHUB_REPOSITORY
$version = $Tag.TrimStart('v')
$dir = Join-Path ([IO.Path]::GetTempPath()) "dms-publish-$version"
New-Item -ItemType Directory -Force $dir | Out-Null

$asset = Join-Path $dir 'downloadmanagersquared.xpi'
Copy-Item $Xpi $asset -Force
$hash = (Get-FileHash -Algorithm SHA256 $asset).Hash.ToLower()
# The update link names the version, so an old updates.json never points at a newer file.
$updates = [ordered]@{
    addons = [ordered]@{
        'downloadmanagersquared@dms' = [ordered]@{
            updates = @([ordered]@{
                version     = $version
                update_link = "https://github.com/$repo/releases/download/$Tag/downloadmanagersquared.xpi"
                update_hash = "sha256:$hash"
            })
        }
    }
}
$json = Join-Path $dir 'updates.json'
[IO.File]::WriteAllText($json, ($updates | ConvertTo-Json -Depth 6), (New-Object Text.UTF8Encoding $false))

# The engine zip carries the signed extension too, so setup can open it in the
# browser straight from disk: one install prompt, the version that matches.
$zipName = 'downloadmanagersquared-windows.zip'
gh release download $Tag --pattern $zipName --dir $dir --clobber --repo $repo
if ($LASTEXITCODE) { throw 'downloading the engine zip failed' }
$zip = Join-Path $dir $zipName
Add-Type -AssemblyName System.IO.Compression, System.IO.Compression.FileSystem
$archive = [IO.Compression.ZipFile]::Open($zip, [IO.Compression.ZipArchiveMode]::Update)
try {
    $old = $archive.GetEntry('downloadmanagersquared.xpi')
    if ($old) { $old.Delete() }
    [IO.Compression.ZipFileExtensions]::CreateEntryFromFile($archive, $asset, 'downloadmanagersquared.xpi') | Out-Null
} finally {
    $archive.Dispose()
}

gh release upload $Tag $asset $json $zip --clobber --repo $repo
if ($LASTEXITCODE) { throw 'uploading the extension failed' }
# The notes hash the files exactly as uploaded.
$notes = Join-Path $dir 'notes.md'
$text = & (Join-Path $PSScriptRoot 'release-notes.ps1') -Tag $Tag -Files $zip, $asset
[IO.File]::WriteAllText($notes, $text, (New-Object Text.UTF8Encoding $false))
gh release edit $Tag --notes-file $notes --draft=false --latest --repo $repo
if ($LASTEXITCODE) { throw 'publishing the release failed' }
Write-Host "Published $Tag"
