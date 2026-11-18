# Uploads -Zip to the Chrome Web Store as the next version of DMS²'s item and
# submits it for review (Chrome Web Store API V2). The item ID is
# ChromiumExtensionID in engine/internal/install/install.go. Needs
# CWS_CLIENT_ID, CWS_CLIENT_SECRET, CWS_REFRESH_TOKEN (an OAuth client and a
# refresh token with the https://www.googleapis.com/auth/chromewebstore scope)
# and CWS_PUBLISHER_ID (Developer Dashboard, Publisher > Settings) in the
# environment. The zip's manifest version must be above the store's.
param([Parameter(Mandatory)][string]$Zip)
$ErrorActionPreference = 'Stop'

$src = Get-Content -Raw (Join-Path $PSScriptRoot '..\..\engine\internal\install\install.go')
if ($src -notmatch 'ChromiumExtensionID\s*=\s*"([a-p]{32})"') { throw 'ChromiumExtensionID not found in install.go' }
$item = $Matches[1]

$token = (Invoke-RestMethod -Method Post -Uri 'https://oauth2.googleapis.com/token' -Body @{
        client_id     = $env:CWS_CLIENT_ID
        client_secret = $env:CWS_CLIENT_SECRET
        refresh_token = $env:CWS_REFRESH_TOKEN
        grant_type    = 'refresh_token'
    }).access_token
$auth = @{ Authorization = "Bearer $token" }
$base = "https://chromewebstore.googleapis.com/v2/publishers/$env:CWS_PUBLISHER_ID/items/$item"

$up = Invoke-RestMethod -Method Post -Uri "https://chromewebstore.googleapis.com/upload/v2/publishers/$env:CWS_PUBLISHER_ID/items/${item}:upload" -Headers $auth -InFile $Zip -ContentType 'application/zip'
$state = $up.uploadState
# The store may process the zip after answering: its state is then fetchStatus's lastAsyncUploadState. At most 5 minutes.
for ($i = 0; $state -eq 'IN_PROGRESS' -and $i -lt 60; $i++) {
    Start-Sleep -Seconds 5
    $state = (Invoke-RestMethod -Uri "${base}:fetchStatus" -Headers $auth).lastAsyncUploadState
}
if ($state -ne 'SUCCEEDED') { throw "the Chrome Web Store didn't take the zip (upload state $state): $($up | ConvertTo-Json -Depth 5 -Compress)" }

$pub = Invoke-RestMethod -Method Post -Uri "${base}:publish" -Headers $auth
Write-Host "Submitted to the Chrome Web Store for review: $($pub | ConvertTo-Json -Depth 5 -Compress)"
