# Downloads the AMO-signed .xpi of -Version (unlisted) to -Out, once AMO has
# signed it. Fails while it is still in review. Needs AMO_JWT_ISSUER and
# AMO_JWT_SECRET in the environment.
param(
    [Parameter(Mandatory)][string]$Version,
    [Parameter(Mandatory)][string]$Out
)
$ErrorActionPreference = 'Stop'
$guid = 'downloadmanagersquared@dms'

function Base64Url([byte[]]$b) { [Convert]::ToBase64String($b).TrimEnd('=').Replace('+', '-').Replace('/', '_') }

# AMO's API takes a short-lived HS256 JWT signed with the API secret.
function AmoToken {
    $now = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds()
    $utf8 = [Text.Encoding]::UTF8
    $header = Base64Url $utf8.GetBytes('{"alg":"HS256","typ":"JWT"}')
    $claims = @{ iss = $env:AMO_JWT_ISSUER; jti = [guid]::NewGuid().ToString(); iat = $now; exp = $now + 240 } | ConvertTo-Json -Compress
    $payload = Base64Url $utf8.GetBytes($claims)
    $hmac = New-Object Security.Cryptography.HMACSHA256 (, $utf8.GetBytes($env:AMO_JWT_SECRET))
    $sig = Base64Url $hmac.ComputeHash($utf8.GetBytes("$header.$payload"))
    "JWT $header.$payload.$sig"
}

$api = "https://addons.mozilla.org/api/v5/addons/addon/$([uri]::EscapeDataString($guid))/versions/?filter=all_with_unlisted&page_size=50"
$list = Invoke-RestMethod -Uri $api -Headers @{ Authorization = (AmoToken) }
$v = $list.results | Where-Object { $_.version -eq $Version } | Select-Object -First 1
if (-not $v) { throw "AMO has no version $Version" }
if ($v.file.status -ne 'public') { throw "version $Version is not signed yet (status: $($v.file.status))" }
Invoke-WebRequest -Uri $v.file.url -Headers @{ Authorization = (AmoToken) } -OutFile $Out
Write-Host "Fetched the signed $Version to $Out"
