# Run by release-gate.yml every 15 minutes. For each draft release vX.Y.Z,
# checks both stores: AMO has approved the listed X.Y.Z (AMO's public API, no
# key) and the Chrome Web Store has it staged (fetchStatus with the read-only
# token, CWS_READ_REFRESH_TOKEN: cws-check.ps1 -ReadOnly). Once both have, it
# starts release-signed.yml on the tag, which publishes the GitHub release and
# then makes the Chrome version live. It starts it once per tag: a failed run
# is re-run by hand. Needs GH_TOKEN with contents: write (drafts are only
# listed to writers) and actions: write.
$ErrorActionPreference = 'Stop'
$repo = $env:GITHUB_REPOSITORY

$list = gh release list --repo $repo --limit 30 --json tagName,isDraft,createdAt
if ($LASTEXITCODE) { throw 'listing the releases failed' }
$drafts = @(($list | ConvertFrom-Json) | Where-Object { $_.isDraft -and $_.tagName -match '^v\d+\.\d+\.\d+$' })
if (-not $drafts) { Write-Host 'No draft release waits.'; exit 0 }

$amo = (Invoke-RestMethod 'https://addons.mozilla.org/api/v5/addons/addon/downloadmanagersquared@dms/versions/?page_size=50').results |
    Where-Object { $_.file.status -eq 'public' } | ForEach-Object version

$cwsAuth = $null
if ($env:CWS_CLIENT_ID -and $env:CWS_CLIENT_SECRET -and $env:CWS_PUBLISHER_ID -and $env:CWS_READ_REFRESH_TOKEN) {
    . (Join-Path $PSScriptRoot 'cws-lib.ps1')
    $cwsAuth = CwsAuth
}

foreach ($d in $drafts) {
    $tag = $d.tagName
    $version = $tag.TrimStart('v')
    $firefox = if ($amo -contains $version) { 'approved' } else { 'not approved yet' }
    $chrome = if ($cwsAuth) { CwsState $version $cwsAuth } else { 'unknown (no CWS_READ_REFRESH_TOKEN repo secret)' }
    Write-Host "${tag}: AMO $firefox, Chrome Web Store $chrome"
    # A staged Chrome submission reverts to a draft after 30 days (developer.chrome.com/docs/webstore/publish).
    if (([datetime]::UtcNow - [datetime]$d.createdAt).TotalDays -gt 25) { echo "::warning::$tag has waited over 25 days: a staged Chrome Web Store version expires after 30." }
    if ($firefox -ne 'approved' -or $chrome -notin 'staged', 'published') { continue }

    $runs = @((gh run list --repo $repo --workflow release-signed.yml --branch $tag --json status,conclusion | ConvertFrom-Json) | Where-Object { $_.conclusion -ne 'cancelled' })
    if ($runs) { echo "::notice::$tag has a release-signed.yml run already ($($runs[0].status) $($runs[0].conclusion)); re-run it by hand if it failed."; continue }
    gh workflow run release-signed.yml --repo $repo --ref $tag -f tag=$tag
    if ($LASTEXITCODE) { throw "starting release-signed.yml on $tag failed" }
    echo "::notice::Both stores approved $tag. Started release-signed.yml on it: approve the release environment to publish."
}
