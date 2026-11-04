# Install to Program Files, elevating only the copy

Date: 2026-11-04
Status: accepted

## Context

DMS² installed itself per-user into `%LOCALAPPDATA%\downloadmanagersquared` with no prompt at all. Users expect programs in Program Files and want to choose the folder. Program Files can only be written as administrator, but everything else an install does is per-user: the native-messaging host key, start at login and the Installed apps entry live under HKEY_CURRENT_USER, and the engine's pipe and proxies only serve the user who runs it.

## Decision

`dms-engine.exe` shows a setup window (folder, Start menu, desktop shortcut, start with Windows) and defaults to `C:\Program Files\downloadmanagersquared`. Only copying the files runs as administrator: if the user can't write the chosen folder, the installing process starts its own exe elevated with `--put-files <folder>`, which writes the exes, the host manifest and the icon and touches no registry. The registry, the shortcuts and the engine itself stay with the user who installed.

The install folder is recorded as the Installed apps entry's `InstallLocation`, which upgrades read to update in place without showing setup again.

Uninstalling deletes DMS²'s own files by name and then the folder only if it is empty, since the user may have picked a folder that holds other things.

## Consequences

- Upgrades and uninstalls of a Program Files install show one UAC prompt each.
- Running the whole setup elevated would put the HKCU entries on the administrator's account when a standard user installs ("over-the-shoulder" elevation); keeping the elevated part to the file copy avoids that.
- The engine can no longer rewrite its host manifest at startup when it lives in Program Files; the install writes it, and the engine only re-points the registry key.
- Existing installs in `%LOCALAPPDATA%` keep upgrading there.
