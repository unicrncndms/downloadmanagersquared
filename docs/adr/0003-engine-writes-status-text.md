# The engine writes the status text the UIs show

Date: 2026-11-04
Status: accepted

## Context

The downloads popup worked out on its own what the engine's state means: when a ready link reads "Downloading", when Backoff reads "Server busy · 2 of 4 connections", which buttons a download offers, whether Remove has a partial file to ask about, and the notes on the progress line ("main connection only", "Asking peers for the torrent's files…"). A standalone GUI for the engine would have had to copy all of it, and the two copies would drift.

## Decision

`download.present` fills in, for every download summary the engine sends, a label and tone for the download and each of its links, the actions it allows, whether a partial file exists, and the progress note. Both HTTP downloads and torrents pass through it. The text is finished English: DMS² is English-only, and the engine already writes the errors, balloons and tray menu users read.

UIs keep only layout: formatting bytes, speeds and ETAs, interface names, and placing the note after the progress numbers (or alone when there are none). Machine `status` values stay on the wire for sorting and filtering; the inputs only the labels need (`busy`, `allowed`, `maxConns`) do not.

## Consequences

- A new UI, or a change to a label, is one place in Go with a table test, not one per UI.
- Translating DMS² would mean sending meanings instead of text, or translating in the engine. Revisit this if translation is ever planned.
- The extension and the engine must agree on the summary's shape; they are released together and the popup already warns when their versions differ.
