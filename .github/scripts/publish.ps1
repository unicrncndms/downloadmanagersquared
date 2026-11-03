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

gh release upload $Tag $asset $json --clobber --repo $repo
if ($LASTEXITCODE) { throw 'uploading the extension failed' }
gh release edit $Tag --draft=false --latest --repo $repo
if ($LASTEXITCODE) { throw 'publishing the release failed' }
Write-Host "Published $Tag"
