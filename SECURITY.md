# Security

## Reporting a vulnerability

Please report it privately: on GitHub, open the repository's **Security** tab and choose **Report a vulnerability**. Don't open a public issue for it. The report stays private until a fix is released.

Supported: the latest release only.

## In scope

- The engine (`dms-engine.exe`), the native-messaging relay (`dms-host.exe`) and the browser extension
- The installer and the release pipeline (what ends up in the zip and the `.xpi` Mozilla signs)
- How DMS² stores your settings, download list, cookies, WireGuard configs and PIA login
- What the log and *Copy diagnostics* leave out at each *Log privacy* level: a password, cookie or token that gets through is a vulnerability

Out of scope: the sites you download from, your VPN provider, and the libraries DMS² uses, unless DMS² uses them unsafely (report those upstream too).

## What DMS² keeps out, and what it doesn't

The engine's named pipe only admits the Windows user the engine runs as; it drops any message over 4 MB, and a request that fails inside the engine gets an error back instead of bringing it down. Each connection's SOCKS5 proxy listens on loopback, looks up the process at the other end of every connection and refuses it unless it belongs to that same user, refusing too when it can't tell. The proxies only connect to public IPv4 addresses, so they can't be used to reach your local network or the PC itself.

DMS² doesn't protect you from programs running as your own user, or from the PC's administrators: both can read your files and talk to the engine as you can. See [PRIVACY.md](PRIVACY.md) for what is stored and where.

## Updates

The extension updates itself from addons.mozilla.org; every update is signed by Mozilla, and Firefox won't install one that isn't. (Builds before 0.12.0 updated from this repository's releases and move to addons.mozilla.org with 0.12.0.) To decide on updates yourself, open `about:addons`, choose downloadmanagersquared, and set **Allow automatic updates** to **Off**. The engine updates only when you press **Update now**: it downloads the release zip from github.com over HTTPS, checks it against the SHA-256 GitHub lists for that file (no checksum, no install), takes only `dms-engine.exe` out of it and runs it, which upgrades the install the usual way and asks before quitting the running engine. Nothing is ever installed automatically; the binaries are not code-signed, and you can still run a newer `dms-engine.exe` yourself instead.

To check that a release's zip, `dms-engine.exe` or `dms-host.exe` was built by this repository's release workflow from the tagged source, run `gh attestation verify <file> --repo unicrncndms/downloadmanagersquared`. It shows the workflow run and commit; it doesn't mean the build is reproducible.
