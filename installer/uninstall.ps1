# Removes the engine and its registrations. Downloaded files and partial
# downloads are left alone; settings stay in %APPDATA%\downloadmanagersquared.
$ErrorActionPreference = 'Stop'
$dest = Join-Path $env:LOCALAPPDATA 'downloadmanagersquared'

Get-Process dms-engine, dms-host -ErrorAction SilentlyContinue | Stop-Process -Force
Remove-Item 'HKCU:\Software\Mozilla\NativeMessagingHosts\downloadmanagersquared' -ErrorAction SilentlyContinue
Remove-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name 'downloadmanagersquared' -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 500
Remove-Item -Recurse -Force $dest -ErrorAction SilentlyContinue
Write-Host 'Uninstalled. Remove the Firefox extension from about:addons.'
