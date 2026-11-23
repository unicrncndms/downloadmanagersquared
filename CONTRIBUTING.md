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

This runs the tests and writes the exes, an unsigned `.xpi`, the Chromium zip and the release zip to `dist\`. Release Firefox only accepts signed extensions; load an unsigned one under `about:debugging` → *This Firefox* → *Load Temporary Add-on*.

Releasing: push a tag `vX.Y.Z`. `release.yml` runs CI, builds, attests the exes and both zips, drafts the release with the engine zip and the Chromium zip, submits the extension to addons.mozilla.org as a *listed* version (no `update_url`: Firefox updates it from AMO) and uploads the Chromium zip to the Chrome Web Store *staged* (`.github/scripts/cws-upload.ps1 -Staged`, Chrome Web Store API V2): once approved, it waits until we make it live. Both stores review, which takes hours to days. `release-gate.yml` checks both every 15 minutes, and once AMO has approved the version and Chrome has it staged, it starts `release-signed.yml` on the tag (the `release` environment asks for approval): that fetches the approved `.xpi` from AMO, attaches it to the draft with its hash in the notes, publishes the release, then makes the staged Chrome version live (`cws-live.ps1`). So the engine is never public before both stores have approved its extension, nor without its `.xpi`. AMO has no "approve now, publish later", so it may go live up to one check before GitHub: see the compatibility rule below. To publish by hand: `gh workflow run release-signed.yml --ref vX.Y.Z -f tag=vX.Y.Z`; it refuses while Chrome hasn't approved. The `release` environment holds `AMO_JWT_ISSUER`, `AMO_JWT_SECRET`, `CWS_CLIENT_ID`, `CWS_CLIENT_SECRET`, `CWS_REFRESH_TOKEN` (scope `https://www.googleapis.com/auth/chromewebstore`) and `CWS_PUBLISHER_ID`; the gate runs on `main`, which can't use them, so it reads AMO's public API and the Chrome Web Store with a read-only token in repo secrets (`CWS_CLIENT_ID`, `CWS_CLIENT_SECRET`, `CWS_PUBLISHER_ID`, `CWS_READ_REFRESH_TOKEN`). `.github/scripts/cws-check.ps1` sets them up: it asks for an OAuth client (type Desktop app) and the publisher ID, gets the refresh token through the browser, checks all four against the store without uploading, and stores them as secrets of the `release` environment; with `-ReadOnly` it gets the read-only token and stores the repo secrets. Don't tag while a Chrome review is still pending: the store takes one submission at a time (if the staged upload fails, upload the zip in the dashboard with *Defer publish* on). A staged Chrome version expires after 30 days. For v0.12.0 only, `release-signed.yml` also attaches `updates.json`, which sends the old self-distributed builds (they carry an `update_url`) to the AMO-signed 0.12.0 file. No later release writes one; each draft carries over the one from the previous published release, so that URL keeps working. Publish a release before tagging the next.

Release notes: before tagging, write and commit `docs/releases/vX.Y.Z.md`; `release.yml` refuses a tag without it. `release-notes.ps1` puts the install line above it and the SHA-256 of each file in a collapsed block below. Format, leaving out empty sections:

```markdown
## New
- Per-IP link resolving added to Chrome and Edge

## Improved
- Updating while a browser is open no longer fails

## Fixed
- Chrome's popup flickered when an (i) tip opened

## Known limitations
- Chromium version does not yet support link resolving per interface
```

Short changelog lines, not sentences explaining the feature. Only what a user notices when using the app: no internals, CI, release workflows, tests, debug logging, store screenshots or small visual fixes nobody would miss. Name the browser when a change is only for one. *Known limitations* lists only gaps that are temporary or planned to close (a feature one browser doesn't have yet, a version mismatch while the stores approve at different times); permanent ones stay in the README's Limitations. Draft from the commits since the last release, `git log --no-merges --format=%s vPREVIOUS..HEAD`, then edit. `docs/releases/v0.13.0.md` is the example.

Compatibility between extension and engine: each release works with the engine of the release before it (extension 0.13 with engine 0.12, extension 1.0 with engine 0.13), because the stores publish the extension before GitHub publishes its engine. So a new engine method or message the extension depends on needs a fallback for the previous engine for one minor version, and the engine must not drop one the previous extension uses. `extension/lib/version.js` (`versionsClash`, `engineBehind`) and `engine/internal/relver` encode it: extension one minor ahead, or at the next major's first minor (1.0 over any 0.x, 2.0 over any 1.x), is no clash, and the popup shows only "A newer engine is on its way"; engine newer than the extension, or two minors apart, is a clash. Both test the cases in `engine/internal/relver/testdata/clash.json`.

Tests alone: `cd engine && go test ./...` and `node --test "extension/test/*.test.js"`. `extension/dev/` has a harness for the popup and settings page (`python extension/dev/serve.py 8765`) and a test site for per-IP links (`python extension/dev/landing.py 8780`). Terms used in the code and docs are in [CONTEXT.md](CONTEXT.md), decisions in [docs/adr/](docs/adr/).

`powershell -ExecutionPolicy Bypass -File test-changed.ps1` runs only the tests for what changed (`-Base <ref>` compares against another commit than HEAD).
`build.ps1 -NoTests` builds without them.

Benchmarks: `cd engine && go test ./internal/<pkg>/ -run xxx -bench . -benchmem` (archive, engine, queue, torrent, tunnel). Profiling: start `dms-engine.exe` with the environment variable `DMS_PROFILE=1` and it writes `cpu.pprof` (start to quit), `heap.pprof` and `mutex.pprof` (at quit) to the data folder, and logs the slowest tick phases and the size of the state to `engine.log` every 10 s. Open them with `go tool pprof`. Off unless the variable is set; files only, no listener. The big UI fixtures are `?fixture=big-queue`, `big-finished` and `big-mixed` in the harness.

The product mark is drawn in `engine/internal/mark` (the tray and setup icon) and mirrored in `extension/icons/icon.svg`. The exe's icon is the committed `engine/cmd/dms-engine/icon_windows_amd64.syso`; after changing the mark regenerate it with `cd engine/cmd/dms-engine && go run genicon.go`, which also writes `extension/icons/icon-{16,32,48,128}.png` (the Chromium build takes no SVG icons).

Data and log live in `%APPDATA%\downloadmanagersquared\` (`settings.json`, `downloads\`, `torrents\`, `sftp.json` (SFTP logins and trusted server keys), `engine.log`). Unreadable files are renamed to `….broken`, with a notification.
