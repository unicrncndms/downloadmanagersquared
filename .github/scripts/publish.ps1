# Publishes the draft release of -Tag, whose -Zip and -Xpi attach.ps1 uploaded
# and the workflow attested. A release is only ever made public here, with its .xpi.
param(
    [Parameter(Mandatory)][string]$Tag,
    [Parameter(Mandatory)][string]$Zip,
    [Parameter(Mandatory)][string]$Xpi
)
$ErrorActionPreference = 'Stop'
$repo = $env:GITHUB_REPOSITORY
# The notes hash the files exactly as uploaded.
$notes = Join-Path (Split-Path -Parent $Zip) 'notes.md'
$text = & (Join-Path $PSScriptRoot 'release-notes.ps1') -Tag $Tag -Files $Zip, $Xpi
[IO.File]::WriteAllText($notes, $text, (New-Object Text.UTF8Encoding $false))
gh release edit $Tag --notes-file $notes --draft=false --latest --repo $repo
if ($LASTEXITCODE) { throw 'publishing the release failed' }
Write-Host "Published $Tag"
