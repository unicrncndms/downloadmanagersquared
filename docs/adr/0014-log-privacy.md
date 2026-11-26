# Log privacy: the log writer leaves out what is the user's own

Date: 2026-11-26
Status: accepted

## Context

People are asked to attach `engine.log` to an issue. The log said it held no URLs, but it did: Go's network errors quote the whole URL (`Get "https://…/file.iso?token=…"`), and os errors quote whole paths, with the user's folder and file names. Split archives were logged by their names, and the resolver logged the text of the buttons it clicked, which can be a file's name. Public IP addresses showed up in network errors. Nobody could send a log without reading it first.

Fixing each `log.Printf` doesn't hold: most of this comes from error strings the engine doesn't write, and every new log line would have to remember the rule.

## Decision

- **The log writer scrubs every line** (`cmd/dms-engine/logfile.go` calls `logprivacy.Scrub`), so every call, now and later, is covered. The few lines the writer can't tell apart are worded at their call: a Set is logged as a hash of its name (`Set.LogName`), an archive's Entry by its file type, the resolver's step text with file names as `x.ext` (`stepText`).
- **Three levels, a setting** (*Settings → About → Log privacy*, `internal/logprivacy`):
  - *Off*: lines as written, as before.
  - *Standard*, the default: a URL keeps its scheme and host; values that look like secrets (password, cookie, token, key…) go; a path keeps its drive and file type (`F:\…\x.part7.rar.dms.part`, `\\…\x.mkv`), a name inside an archive's folders too (`…/x.bin`); public IPv4 and IPv6 addresses go, private, VPN and link-local ones stay to tell lines apart. The data folder is written as `%DATA%`.
  - *Increased*: Standard, and the hosts and every remaining name with a file type or a host's ending (`x.rar`, `x.com`).
- **Standard keeps the sites.** Most problems are one site's (it refuses ranges, it throttles, its links expire), and they can't be found without its name. Someone who wants to post a log in public picks *Increased*.
- **DMS²'s own code stays readable at every level**: its pages' addresses (`https://dms.invalid/ui/card.js:120` in a script error) and Go source files (`set.go:412` in a crash). They say nothing about the user, and without them a page error or a crash can't be found.
- **Copy diagnostics follows the same level**: its report goes through `Scrub` and the file's name keeps only its type. At *Off* it keeps what it always left out (passwords, query strings, cookies).
- **The level is an atomic** (`logprivacy.Set`/`Get`), not read from the engine's settings: the writer can run while the engine holds its lock.

## Consequences

- Lines already written stay as they were; changing the level only changes new lines. `engine.log.old` too.
- `crash.log` (Go's crash traces) isn't scrubbed. It holds the program's own code and memory addresses, not the user's data.
- Standard isn't airtight: a file's name written without a folder (some errors do) still shows, and so does the first word of a folder name with a space. *Increased* leaves out every such name.
- *Increased* also mangles code names in error text that look like file names (`d.links` becomes `x.links`).
- Masked addresses can't be compared: two tunnels with the same exit IP both read `<public IPv4>`. A stable code per address would need a random key per install, as an IPv4 hash can be reversed by trying them all.
