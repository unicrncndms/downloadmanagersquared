# Makes the staged (approved) -Version live in the Chrome Web Store, and checks
# it went live. Done already if it is published. The API reference doesn't name
# the call for a staged item: :publish says it skips review for an item "staged
# from a previous submission", so this calls :publish again. Environment: see
# cws-lib.ps1 (needs CWS_REFRESH_TOKEN, the write scope).
param([Parameter(Mandatory)][string]$Version)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'cws-lib.ps1')
$auth = CwsAuth

$state = CwsState $Version $auth
if ($state -eq 'published') { Write-Host "$Version is already live in the Chrome Web Store"; exit 0 }
if ($state -ne 'staged') { throw "$Version is $state in the Chrome Web Store, not staged: nothing to make live" }

$body = @{ publishType = 'DEFAULT_PUBLISH' } | ConvertTo-Json -Compress
$pub = Invoke-RestMethod -Method Post -Uri "${CwsBase}:publish" -Headers $auth -Body $body -ContentType 'application/json'
Write-Host "Asked the Chrome Web Store to publish the staged $Version`: $($pub | ConvertTo-Json -Depth 5 -Compress)"
for ($i = 0; $i -lt 24; $i++) {
    if ((CwsState $Version $auth) -eq 'published') { Write-Host "$Version is live in the Chrome Web Store"; exit 0 }
    Start-Sleep -Seconds 5
}
throw "$Version didn't go live in the Chrome Web Store within 2 minutes (state: $(CwsState $Version $auth)). Press Publish on the item in the Developer Dashboard."
