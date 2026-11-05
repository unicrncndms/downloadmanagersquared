# Attaches the signed extension and its updates.json to the draft release of
# -Tag and puts the extension into its engine zip. The release stays a draft:
# the workflow attests the zip as uploaded, then publish.ps1 makes it public.
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

# The engine zip carries the signed extension too, so setup can open it in the
# browser straight from disk: one install prompt, the version that matches.
$zipName = 'downloadmanagersquared-windows.zip'
gh release download $Tag --pattern $zipName --dir $dir --clobber --repo $repo
if ($LASTEXITCODE) { throw 'downloading the engine zip failed' }
$zip = Join-Path $dir $zipName
Add-Type -AssemblyName System.IO.Compression, System.IO.Compression.FileSystem
$archive = [IO.Compression.ZipFile]::Open($zip, [IO.Compression.ZipArchiveMode]::Update)
try {
    $old = $archive.GetEntry('downloadmanagersquared.xpi')
    if ($old) { $old.Delete() }
    [IO.Compression.ZipFileExtensions]::CreateEntryFromFile($archive, $asset, 'downloadmanagersquared.xpi') | Out-Null
} finally {
    $archive.Dispose()
}

gh release upload $Tag $asset $json $zip --clobber --repo $repo
if ($LASTEXITCODE) { throw 'uploading the extension failed' }
# The files exactly as uploaded, for the attestation and publish.ps1.
if ($env:GITHUB_OUTPUT) {
    # No BOM: Windows PowerShell's utf8 would put one before the first name.
    [IO.File]::AppendAllText($env:GITHUB_OUTPUT, "zip=$zip`nxpi=$asset`n", (New-Object Text.UTF8Encoding $false))
}
Write-Host "Attached the signed extension to $Tag"
