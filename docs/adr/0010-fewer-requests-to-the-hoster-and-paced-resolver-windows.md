# Fewer requests to the hoster, and paced resolver windows

Date: 2026-11-15
Status: accepted

## Context

Hosters rate-limit by IP and by account. The engine sent more requests than it had to, and the extension opened as many resolver windows for one hoster as there were rejected interfaces. Five candidates were weighed; the requests the engine causes per event are pinned by `engine/internal/download/requestcount_test.go`.

## Decision

Taken:

- **Expiry skips the probe of the old link.** A 403/410 on a data GET, with a landing page known, goes straight to a resolve (`workerFailedLocked`). Saves one request per expiry. Without a landing page the old link is still probed, as before. A mirror is not resolved: it has no landing page.
- **A pasted landing page is probed on one interface.** The source link is probed on the main interface first. If it is a page, the other interfaces go straight to a resolve and never probe it (`pageGuessLocked`, `decidePageHitsLocked`). If it is the file, they probe right after, so a file link costs the same number of requests and only starts a round trip later on the non-main interfaces. Known limit: if the file link gives the main interface an error page (an IP-bound link), the link is taken for a landing page and every interface resolves from it. Before, another interface that got the file ruled that out.
- **At most 2 resolver windows per page host** (`MAX_WINDOWS` in `extension/lib/route.js`, used by `startOrQueue`). The rest wait and start as a window ends, by success, failure or timeout. The pacing is in the extension: the engine asks for every rejected interface at once and the extension already knows what is open.
- **A refused data GET counts as an HTTP error.** A link whose probe passes every time while its data GET gets 403, with no landing page, looped probe/403 forever. `Link.expired` now adds to `HTTPErrors` (reset only when data arrives), so the link fails after `maxHTTPErrors` with "the link keeps being refused". With a landing page the resolve cap (`maxResolves`) already ends it.

Rejected:

- **Non-main interfaces wait for the main probe, then resolve without probing the source link.** It costs a resolve (a browser window) for every plain server that works from every IP, to save a probe. Hosters can't be told from plain servers without a recipe per host.
- **No probe after a resolve.** The probe also sets the address family, finds mirrors and checks size and version. Dropping it is too risky without live hosters to test against.

## Consequences

- A later change that raises the requests per start, expiry, pasted page or refused-data loop fails `requestcount_test.go`.
- With a cap of 2, a download with many interfaces resolves them in waves; each wave still needs a window's time.
