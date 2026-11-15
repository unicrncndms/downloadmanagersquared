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

## Addendum: watch and unwatch

The extension's background says `unwatch` after hello while no page of the extension is open, and `watch` when the first one opens. The engine sends the whole `state` event only to a client that watches (every client does until it says `unwatch`, so older extensions and the Downloads window are unchanged); a client that does not gets a small `badge` event (`running`, `needs`: the ids that need the user) when it changes. `watch` answers with the current state at once. An older engine answers both with an unknown-method error, which the background ignores. Neither method is version-gated (`relver`): a missing one only costs the saving.

## Addendum: removeMany

Removing a selection is one `removeMany` call (`ids`, `deleteFile`): the engine takes the queue's save lock once and walks the list once, and skips an id that is gone. An older engine answers it with an unknown-method error, and the page then calls `remove` per id as before; an older extension keeps calling `remove`. After a call that changes the list the engine also pushes the state within about 75 ms instead of waiting for the tick.
