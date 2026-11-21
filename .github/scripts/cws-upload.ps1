# Uploads -Zip to the Chrome Web Store as the next version of DMS²'s item and
# submits it for review (Chrome Web Store API V2). With -Staged, an approved
# version waits (state STAGED) until cws-live.ps1 makes it live: release.yml
# uploads staged so Chrome goes live with the GitHub release, not before it.
# Environment: see cws-lib.ps1 (needs CWS_REFRESH_TOKEN, the write scope). The
# zip's manifest version must be above the store's.
param([Parameter(Mandatory)][string]$Zip, [switch]$Staged)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'cws-lib.ps1')
$auth = CwsAuth

$up = Invoke-RestMethod -Method Post -Uri "https://chromewebstore.googleapis.com/upload/v2/publishers/$env:CWS_PUBLISHER_ID/items/${CwsItem}:upload" -Headers $auth -InFile $Zip -ContentType 'application/zip'
$state = $up.uploadState
# The store may process the zip after answering: its state is then fetchStatus's lastAsyncUploadState. At most 5 minutes.
for ($i = 0; $state -eq 'IN_PROGRESS' -and $i -lt 60; $i++) {
    Start-Sleep -Seconds 5
    $state = (Invoke-RestMethod -Uri "${CwsBase}:fetchStatus" -Headers $auth).lastAsyncUploadState
}
if ($state -ne 'SUCCEEDED') { throw "the Chrome Web Store didn't take the zip (upload state $state): $($up | ConvertTo-Json -Depth 5 -Compress)" }

$body = @{ publishType = $(if ($Staged) { 'STAGED_PUBLISH' } else { 'DEFAULT_PUBLISH' }) } | ConvertTo-Json -Compress
$pub = Invoke-RestMethod -Method Post -Uri "${CwsBase}:publish" -Headers $auth -Body $body -ContentType 'application/json'
Write-Host "Submitted to the Chrome Web Store for review$(if ($Staged) { ', staged' }): $($pub | ConvertTo-Json -Depth 5 -Compress)"
