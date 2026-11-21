# Shared by the cws-*.ps1 scripts (dot-source it): the Chrome Web Store item,
# an access token, and where a version stands in the store (API V2). Needs
# CWS_CLIENT_ID, CWS_CLIENT_SECRET and CWS_PUBLISHER_ID in the environment, and
# CWS_REFRESH_TOKEN (scope chromewebstore) or, for reading only,
# CWS_READ_REFRESH_TOKEN (scope chromewebstore.readonly: cws-check.ps1 -ReadOnly).

$src = Get-Content -Raw (Join-Path $PSScriptRoot '..\..\engine\internal\install\install.go')
if ($src -notmatch 'ChromiumExtensionID\s*=\s*"([a-p]{32})"') { throw 'ChromiumExtensionID not found in install.go' }
$CwsItem = $Matches[1]
$CwsBase = "https://chromewebstore.googleapis.com/v2/publishers/$env:CWS_PUBLISHER_ID/items/$CwsItem"

function CwsAuth {
    $refresh = if ($env:CWS_REFRESH_TOKEN) { $env:CWS_REFRESH_TOKEN } else { $env:CWS_READ_REFRESH_TOKEN }
    $token = (Invoke-RestMethod -Method Post -Uri 'https://oauth2.googleapis.com/token' -Body @{
            client_id     = $env:CWS_CLIENT_ID
            client_secret = $env:CWS_CLIENT_SECRET
            refresh_token = $refresh
            grant_type    = 'refresh_token'
        }).access_token
    @{ Authorization = "Bearer $token" }
}

# Where -Version stands: published, staged (approved, waiting for cws-live.ps1),
# pending (in review), rejected, cancelled, or none (not submitted). A revision
# counts only if its crxVersion is -Version, so an older staged one isn't taken for it.
function CwsState([string]$Version, $Auth) {
    $st = Invoke-RestMethod -Uri "${CwsBase}:fetchStatus" -Headers $Auth
    if ($st.publishedItemRevisionStatus.distributionChannels.crxVersion -contains $Version) { return 'published' }
    $sub = $st.submittedItemRevisionStatus
    if ($sub.distributionChannels.crxVersion -notcontains $Version) { return 'none' }
    switch ($sub.state) {
        'STAGED' { 'staged' }
        'PENDING_REVIEW' { 'pending' }
        'PUBLISHED' { 'published' }
        'REJECTED' { 'rejected' }
        'CANCELLED' { 'cancelled' }
        default { "$($sub.state)".ToLower() }
    }
}
