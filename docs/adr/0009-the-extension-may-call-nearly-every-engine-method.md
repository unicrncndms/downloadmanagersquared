# The extension may call nearly every engine method

Date: 2026-11-14
Status: accepted

## Context

The engine's pipe is open to its own user only (ADR 0002, ADR 0004). Every client of it was fully trusted: the extension, through `dms-host.exe`'s relay; the Downloads window, in this process; and the installer and tray, which call `quit`, `restartCost` and `extensionBrowsers`. The extension is the one client that handles web content, so the question was whether the host should give it an allowlist.

The extension's pages are also the Downloads window's pages (ADR 0004). The settings page calls `setSettings`, `setAutostart`, `importTunnel`, `setPia`, `openLog` and `openDataFolder`, the dangerous-looking ones, as part of its normal work. `extension/test/protocol.test.js` lists every method the extension calls.

## Decision

No allowlist in the host's relay: any method the pages call must work, and the pages need almost all of them.

Two limits that cost nothing:

- `quit` and `restartCost` are for the installer and the tray. A connection that has said hello as the extension gets an error from them.
- `dms-host.exe` sets `params.browser` of the extension's hello from its own parent process only. With no parent exe found, it drops what the extension sent, since the engine starts the browser that names.

## Consequences

- A compromised extension can still change settings, add a login item, import a tunnel or change the PIA login, as the settings page can. Its install is the user's trust decision; Firefox's review of the extension is the protection.
- A new method needs no entry in a list to work for the extension. `protocol.test.js` fails when the extension calls a name the engine does not have, and when the engine has a method nothing calls and that is not listed there as one for other clients.
