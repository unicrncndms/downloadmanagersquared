# The updates.json that the self-distributed Firefox builds (0.11.x and earlier,
# which carry an update_url) fetch from releases/latest/download/updates.json.
# From 0.12.0 the extension is AMO-listed without update_url, so this file has
# one last job: send those builds to the AMO-approved 0.12.0 .xpi, copied onto
# its GitHub release. It is written once, for $last, and never changes after.
#   -Xpi given (release-signed.yml, once AMO approved it): writes Dir\updates.json
#     for $last only; any other tag gets nothing.
#   -Xpi absent (release.yml): any other tag gets the previous latest release's
#     updates.json, so the URL keeps working while it is the latest release.
param(
    [Parameter(Mandatory)][string]$Tag,
    [Parameter(Mandatory)][string]$Dir,
    [string]$Xpi
)
$ErrorActionPreference = 'Stop'
$last = '0.12.0'
$version = $Tag.TrimStart('v')
New-Item -ItemType Directory -Force $Dir | Out-Null
$json = Join-Path $Dir 'updates.json'
Remove-Item $json -ErrorAction SilentlyContinue

if ($Xpi) {
    if ($version -ne $last) { return }
    $hash = (Get-FileHash -Algorithm SHA256 $Xpi).Hash.ToLower()
    $updates = [ordered]@{
        addons = [ordered]@{
            'downloadmanagersquared@dms' = [ordered]@{
                updates = @([ordered]@{
                    version     = $version
                    update_link = "https://github.com/$env:GITHUB_REPOSITORY/releases/download/$Tag/downloadmanagersquared.xpi"
                    update_hash = "sha256:$hash"
                })
            }
        }
    }
    [IO.File]::WriteAllText($json, ($updates | ConvertTo-Json -Depth 6), (New-Object Text.UTF8Encoding $false))
} elseif ($version -ne $last) {
    # No previous updates.json (or no release) is not an error: nothing to carry.
    & { $ErrorActionPreference = 'Continue'; gh release download --pattern updates.json --dir $Dir --clobber --repo $env:GITHUB_REPOSITORY 2>$null }
    $global:LASTEXITCODE = 0
}
