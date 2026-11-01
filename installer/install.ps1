# Installs the engine for the current user (no admin rights needed):
# copies the executables, registers the Firefox native-messaging host,
# enables start-at-login and starts the engine.
$ErrorActionPreference = 'Stop'
$src = Join-Path (Split-Path $PSScriptRoot -Parent) 'dist'
$dest = Join-Path $env:LOCALAPPDATA 'downloadmanagersquared'
$extensionId = 'downloadmanagersquared@dms'

foreach ($f in 'dms-engine.exe', 'dms-host.exe') {
    if (-not (Test-Path (Join-Path $src $f))) { throw "$f not found in $src - run build.ps1 first." }
}

Get-Process dms-engine, dms-host -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Milliseconds 500

New-Item -ItemType Directory -Force $dest | Out-Null
Copy-Item (Join-Path $src 'dms-engine.exe'), (Join-Path $src 'dms-host.exe') $dest -Force

$manifest = [ordered]@{
    name               = 'downloadmanagersquared'
    description        = 'downloadmanagersquared engine'
    path               = (Join-Path $dest 'dms-host.exe')
    type               = 'stdio'
    allowed_extensions = @($extensionId)
}
$manifestPath = Join-Path $dest 'native-host.json'
[IO.File]::WriteAllText($manifestPath, ($manifest | ConvertTo-Json), (New-Object Text.UTF8Encoding $false))

$key = 'HKCU:\Software\Mozilla\NativeMessagingHosts\downloadmanagersquared'
New-Item -Force $key | Out-Null
Set-Item $key -Value $manifestPath

Set-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name 'downloadmanagersquared' -Value ('"' + (Join-Path $dest 'dms-engine.exe') + '"')

Start-Process (Join-Path $dest 'dms-engine.exe')
Write-Host "Installed to $dest. The engine is running in the tray."
Write-Host 'Now install the Firefox extension (see README.md).'
