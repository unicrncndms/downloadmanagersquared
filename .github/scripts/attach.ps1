# Attaches the AMO-approved -Xpi to the already public release of -Tag (and, for
# the one migration version, its updates.json: see updates-json.ps1) and adds
# its SHA-256 to the release notes.
param(
    [Parameter(Mandatory)][string]$Tag,
    [Parameter(Mandatory)][string]$Xpi
)
$ErrorActionPreference = 'Stop'
$repo = $env:GITHUB_REPOSITORY
$version = $Tag.TrimStart('v')
$dir = Join-Path ([IO.Path]::GetTempPath()) "dms-attach-$version"
New-Item -ItemType Directory -Force $dir | Out-Null

$asset = Join-Path $dir 'downloadmanagersquared.xpi'
Copy-Item $Xpi $asset -Force
& (Join-Path $PSScriptRoot 'updates-json.ps1') -Tag $Tag -Dir $dir -Xpi $asset
$files = @($asset) + @(Get-ChildItem (Join-Path $dir 'updates.json') -ErrorAction SilentlyContinue | ForEach-Object FullName)
gh release upload $Tag @files --clobber --repo $repo
if ($LASTEXITCODE) { throw 'uploading the extension failed' }

# The notes list every attached file's hash: fetch the zips (their hashes are those of the upload).
gh release download $Tag --pattern '*.zip' --dir $dir --clobber --repo $repo
if ($LASTEXITCODE) { throw 'downloading the zips failed' }
$notes = Join-Path $dir 'notes.md'
$zips = @(Get-ChildItem (Join-Path $dir '*.zip') | ForEach-Object FullName)
$text = & (Join-Path $PSScriptRoot 'release-notes.ps1') -Tag $Tag -Files ($zips + $asset)
[IO.File]::WriteAllText($notes, $text, (New-Object Text.UTF8Encoding $false))
gh release edit $Tag --notes-file $notes --repo $repo
if ($LASTEXITCODE) { throw 'updating the release notes failed' }
Write-Host "Attached the AMO-signed extension to $Tag"
