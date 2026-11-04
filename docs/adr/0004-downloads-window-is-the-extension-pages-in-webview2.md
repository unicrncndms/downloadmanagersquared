# The Downloads window is the extension's pages in WebView2, inside the engine

Date: 2026-11-04
Status: accepted

## Context

With Firefox closed there is no way to watch downloads or change settings: the tray's "Open downloads" starts Firefox to show the extension's popup. The engine needs a window of its own.

The extension's downloads popup and settings page are about 800 lines of HTML and JS with a UI harness. The engine builds as pure Go (no cgo). The pipe serves only its own user, and nothing listens on a network port for the UI.

Alternatives considered:

1. **A native Go UI** (Win32 by hand like setup, walk, Gio, or Fyne). It would rebuild every view and drift from the extension's on every change; Fyne also needs cgo.
2. **Pages served over localhost HTTP, opened in any browser.** It needs a port other local users can reach, and a browser to be running.
3. **A separate `dms-gui.exe` over the pipe.** It isolates a webview crash from downloads, but adds a process to install, upgrade and quit, and a full pipe client in Go.

## Decision

The Downloads window is the extension's own pages shown in WebView2 (`jchv/go-webview2`, which needs no cgo) by `dms-engine.exe`, beside the tray. The webview runs its message loop on a locked thread of its own, next to systray's on the main thread. The branch `prototype/downloads-window` showed this works: it builds with `CGO_ENABLED=0`, open, close, reopen and quit-while-open are clean, and closing frees WebView2's processes within seconds.

- The pages reach the engine only through a small `Host` module. It has three adapters: the extension (runtime port), the Downloads window and the UI harness. `host.js` picks the adapter by detecting where it runs.
- The window's adapter talks the same JSON-lines protocol as every other client, over an in-memory pipe that `ipc` serves like the named pipe.
- `build.ps1` copies the extension's `ui/` into the engine before `go build`, and the exe embeds it.
- The tray, and a second launch of the engine, always open the Downloads window, never Firefox. Firefox opens only if WebView2 is missing.
- Closing the window destroys the webview and leaves the engine in the tray.
- Downloads added from the window send no user agent, so the engine uses the browser's last one.

## Consequences

- One set of pages for the extension and the window: a UI change is made once.
- WebView2's processes cost about 100 MB while the window is open, and nothing once it is closed.
- A webview crash shares the engine's process. If that proves real, moving the window into its own exe means only a new pipe-based `Host` adapter.
- Settings the browser keeps must move to the engine to show in the window. The VPN provider choice moves; Recipes stay in Firefox, and the window says so.
