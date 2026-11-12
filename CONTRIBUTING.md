# Contributing

Bugs and ideas: open an issue. Questions: Discussions. Vulnerabilities: see [SECURITY.md](SECURITY.md).

## How it fits together

```
browser extension ──native messaging──> dms-host.exe ──named pipe──> dms-engine.exe (tray)
  UI, takes over downloads                 (relay)                     downloads, queue, torrents,
  per-IP links in containers ───────── container traffic ──────>     one SOCKS5 proxy per connection
```

- **Engine** (`engine/`, Go): pins sockets to each connection and splits files into chunks. A connection that finishes takes over the far end of the slowest chunk, so faster connections carry more and nothing is fetched twice. A Download as folder reads the archive's list of files first and fetches front to back (`engine/internal/archive`, [ADR 0006](docs/adr/0006-download-as-folder-frees-the-archive-while-downloading.md)).
- **Extension** (`extension/`, plain JavaScript, no build step): takes over downloads, holds the UI, and opens sites in a temporary container per connection, routed through that connection's proxy. The engine's Downloads window shows the same pages in WebView2.

Building needs Go (`winget install GoLang.Go`), and Node.js for the extension's tests:

```bash
powershell -ExecutionPolicy Bypass -File build.ps1
```

This runs the tests and writes the exes, an unsigned `.xpi` and the release zip to `dist\`. Release Firefox only accepts signed extensions; load an unsigned one under `about:debugging` → *This Firefox* → *Load Temporary Add-on*.

Tests alone: `cd engine && go test ./...` and `node --test "extension/test/*.test.js"`. `extension/dev/` has a harness for the popup and settings page (`python extension/dev/serve.py 8765`) and a test site for per-IP links (`python extension/dev/landing.py 8780`). Terms used in the code and docs are in [CONTEXT.md](CONTEXT.md), decisions in [docs/adr/](docs/adr/).

Data and log live in `%APPDATA%\downloadmanagersquared\` (`settings.json`, `downloads\`, `torrents\`, `engine.log`). Unreadable files are renamed to `….broken`, with a notification.
