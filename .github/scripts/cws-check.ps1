# Sets up the four Chrome Web Store values cws-upload.ps1 needs: asks for the
# OAuth client (type Desktop app) and the publisher ID, gets the refresh token
# itself (opens Google's consent page in the browser, takes the answer on a
# port of 127.0.0.1), checks them against the store without uploading, and on
# success stores them as secrets of the repo's `release` environment
# (gh secret set). A value already in the environment (CWS_CLIENT_ID,
# CWS_CLIENT_SECRET, CWS_REFRESH_TOKEN, CWS_PUBLISHER_ID) isn't asked for.
#   powershell -ExecutionPolicy Bypass -File .github\scripts\cws-check.ps1
# With -ReadOnly it gets a token that can only read the store
# (chromewebstore.readonly) and stores it, with the client and publisher ID, as
# repo secrets (CWS_READ_REFRESH_TOKEN for the token): release-gate.yml runs on
# main, which can't use the release environment's secrets.
param(
    [switch]$NoStore, # only check, don't set the secrets
    [switch]$ReadOnly
)
$ErrorActionPreference = 'Stop'

function Ask([string]$name, [string]$label, [bool]$hidden) {
    $v = [Environment]::GetEnvironmentVariable($name)
    if (-not $v) {
        if ($hidden) {
            $s = Read-Host "$label (hidden)" -AsSecureString
            $v = [Runtime.InteropServices.Marshal]::PtrToStringBSTR([Runtime.InteropServices.Marshal]::SecureStringToBSTR($s))
        } else {
            $v = Read-Host $label
        }
    }
    $v.Trim()
}

# Google's answer to a failed request: PowerShell 7 has it in ErrorDetails, 5.1 only in the response stream.
function Why($err) {
    if ($err.ErrorDetails.Message) { return $err.ErrorDetails.Message -replace '\s+', ' ' }
    try {
        $r = New-Object IO.StreamReader ($err.Exception.Response.GetResponseStream())
        $r.BaseStream.Position = 0
        return $r.ReadToEnd() -replace '\s+', ' '
    } catch { return "$err" }
}

function B64Url([byte[]]$b) { [Convert]::ToBase64String($b).TrimEnd('=').Replace('+', '-').Replace('/', '_') }

# The refresh token by Google's flow for desktop apps: consent in the browser,
# the code back to a loopback port, PKCE so only this run can redeem it.
function RefreshToken($id, $secret) {
    $l = New-Object Net.Sockets.TcpListener ([Net.IPAddress]::Loopback, 0)
    $l.Start()
    try {
        $redirect = "http://127.0.0.1:$($l.LocalEndpoint.Port)"
        $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
        $buf = New-Object byte[] 48; $rng.GetBytes($buf); $verifier = B64Url $buf
        $rng.GetBytes($buf); $state = B64Url $buf
        $challenge = B64Url ([Security.Cryptography.SHA256]::Create().ComputeHash([Text.Encoding]::ASCII.GetBytes($verifier)))
        $q = @{
            client_id = $id; redirect_uri = $redirect; response_type = 'code'
            scope = $scope
            access_type = 'offline'; prompt = 'consent'
            code_challenge = $challenge; code_challenge_method = 'S256'; state = $state
        }
        $url = 'https://accounts.google.com/o/oauth2/v2/auth?' + (($q.GetEnumerator() | ForEach-Object { "$($_.Key)=$([Uri]::EscapeDataString($_.Value))" }) -join '&')
        Write-Host "Opening Google's consent page. Sign in with the account that owns the Chrome Web Store item."
        Write-Host "  On 'Google hasn't verified this app': Advanced > Go to ... (unsafe) > Continue."
        Write-Host "  If no browser opens: $url"
        Start-Process $url
        while ($true) {
            $c = $l.AcceptTcpClient()
            $stream = $c.GetStream()
            $line = (New-Object IO.StreamReader $stream).ReadLine() # GET /?state=...&code=... HTTP/1.1
            $params = @{}
            if ($line -match '^GET /\?(\S*)') {
                foreach ($kv in $Matches[1] -split '&') {
                    $k, $v = $kv -split '=', 2
                    $params[$k] = [Uri]::UnescapeDataString(($v -replace '\+', ' '))
                }
            }
            $done = $params.code -or $params.error
            $html = if ($params.code) { 'DMS&sup2;: done, you can close this tab.' } elseif ($params.error) { "DMS&sup2;: Google said $($params.error)." } else { '' }
            $body = [Text.Encoding]::UTF8.GetBytes("<!doctype html><meta charset=utf-8><p>$html")
            $head = [Text.Encoding]::ASCII.GetBytes("HTTP/1.1 $(if ($done) { '200 OK' } else { '404 Not Found' })`r`nContent-Type: text/html; charset=utf-8`r`nContent-Length: $($body.Length)`r`nConnection: close`r`n`r`n")
            $stream.Write($head, 0, $head.Length); $stream.Write($body, 0, $body.Length); $c.Close()
            if ($done) { break } # anything else (a favicon) is not the answer
        }
    } finally { $l.Stop() }
    if ($params.error) { throw "Google refused the consent: $($params.error) (an app still in Testing allows only its test users: publish it)" }
    if ($params.state -ne $state) { throw 'the answer from the browser is not for this run' }
    $t = Invoke-RestMethod -Method Post -Uri 'https://oauth2.googleapis.com/token' -Body @{
        code = $params.code; client_id = $id; client_secret = $secret; redirect_uri = $redirect
        grant_type = 'authorization_code'; code_verifier = $verifier
    }
    if (-not $t.refresh_token) { throw 'Google gave no refresh token' }
    $t.refresh_token
}

$scope = 'https://www.googleapis.com/auth/chromewebstore' + $(if ($ReadOnly) { '.readonly' } else { '' })
$tokenName = if ($ReadOnly) { 'CWS_READ_REFRESH_TOKEN' } else { 'CWS_REFRESH_TOKEN' }
$vals = [ordered]@{
    CWS_CLIENT_ID     = Ask 'CWS_CLIENT_ID' 'OAuth client ID (...apps.googleusercontent.com)' $false
    CWS_CLIENT_SECRET = Ask 'CWS_CLIENT_SECRET' 'OAuth client secret' $true
    $tokenName        = [Environment]::GetEnvironmentVariable($tokenName)
    CWS_PUBLISHER_ID  = Ask 'CWS_PUBLISHER_ID' 'Publisher ID (dashboard, Publisher > Settings)' $false
}
if (-not $vals[$tokenName]) {
    try { $vals[$tokenName] = RefreshToken $vals.CWS_CLIENT_ID $vals.CWS_CLIENT_SECRET }
    catch { Write-Host "FAILED: no refresh token. $(Why $_)" -ForegroundColor Red; exit 1 }
    Write-Host "OK: got a refresh token." -ForegroundColor Green
}

$src = Get-Content -Raw (Join-Path $PSScriptRoot '..\..\engine\internal\install\install.go')
if ($src -notmatch 'ChromiumExtensionID\s*=\s*"([a-p]{32})"') { throw 'ChromiumExtensionID not found in install.go' }
$item = $Matches[1]

try {
    $token = (Invoke-RestMethod -Method Post -Uri 'https://oauth2.googleapis.com/token' -Body @{
            client_id = $vals.CWS_CLIENT_ID; client_secret = $vals.CWS_CLIENT_SECRET
            refresh_token = $vals[$tokenName]; grant_type = 'refresh_token'
        }).access_token
} catch {
    Write-Host "FAILED: Google refused the client ID, secret or refresh token." -ForegroundColor Red
    Write-Host "  invalid_client: ID or secret wrong. invalid_grant: refresh token wrong, revoked, or from an app still in Testing that expired."
    Write-Host "  unauthorized_client: the refresh token was made for another client: unset $tokenName and run this again to get a new one."
    Write-Host "  $(Why $_)"
    exit 1
}
Write-Host "OK: client and refresh token give an access token." -ForegroundColor Green

try {
    $st = Invoke-RestMethod -Uri "https://chromewebstore.googleapis.com/v2/publishers/$($vals.CWS_PUBLISHER_ID)/items/${item}:fetchStatus" -Headers @{ Authorization = "Bearer $token" }
} catch {
    $code = $_.Exception.Response.StatusCode.value__
    Write-Host "FAILED ($code): the Chrome Web Store refused the status request for item $item." -ForegroundColor Red
    Write-Host "  403: the API isn't enabled in the Cloud project, the token lacks the chromewebstore scope, or the account doesn't own the item."
    Write-Host "  404: the publisher ID is wrong."
    Write-Host "  $(Why $_)"
    exit 1
}
$pub = $st.publishedItemRevisionStatus.state
$sub = $st.submittedItemRevisionStatus.state
Write-Host "OK: item $item. Published: $(if ($pub) { $pub } else { 'none' }). In review: $(if ($sub) { $sub } else { 'none' })." -ForegroundColor Green
if ($sub -eq 'PENDING_REVIEW') { Write-Host "  A review is pending: a release run now would fail at the upload. Wait until it is through." -ForegroundColor Yellow }

if ($NoStore) { exit 0 }
$where = if ($ReadOnly) { 'the repo' } else { "the 'release' environment" }
$envArgs = if ($ReadOnly) { @() } else { @('--env', 'release') }
if (-not (Get-Command gh -ErrorAction SilentlyContinue)) { Write-Host "gh not found: set the four secrets of $where by hand."; exit 0 }
if ((Read-Host "Store the four values as secrets of $where? (y/n)") -ne 'y') { exit 0 }
foreach ($k in $vals.Keys) {
    $vals[$k] | gh secret set $k @envArgs # from stdin: the value is never on a command line
    if ($LASTEXITCODE) { throw "gh secret set $k failed" }
}
Write-Host "Stored. gh secret list $envArgs shows them." -ForegroundColor Green
