# Secrets the engine saves are encrypted with DPAPI

Date: 2026-11-05
Status: accepted

## Context

The engine already encrypted the PIA login with DPAPI, but kept each download's cookies in plain text in its saved state until the download completed. Cookies are often login sessions, so they are just as sensitive, and a stalled or forgotten download kept them on disk indefinitely. Both outside reviews of DMS² named this as their main concrete concern.

## Decision

Anything secret the engine writes to disk is encrypted with DPAPI for the current Windows user (`winutil.Protect`), never written readable. Today that is the PIA login and the cookies of a download and of each of its links.

Cookies use `download.Secret`, a string that encrypts itself when saved as JSON (`"dpapi:<base64>"`) and is plain in memory. It reads a plain value too, as older versions saved it, and encrypts it on the next save. A value that can't be decrypted (state copied from another user or PC) counts as no cookie: the download goes on without it rather than being set aside as broken. If encryption fails, saving fails; it never falls back to plain text.

Cookies are still cleared from a download once nothing is left to resume, and never logged.

## Consequences

- A new secret that the engine saves (a token, another provider's login) uses `Secret` or `winutil.Protect` the same way.
- DPAPI keeps out other users and anyone reading the disk without your Windows login. It does not keep out programs running as you, as PRIVACY.md says.
- Saved downloads can't be moved to another user or PC with their cookies; they resume without them, which may need the landing page again.
