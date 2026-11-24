# Each line is its own peer in a torrent's swarm

Date: 2026-11-23
Status: accepted

## Context

A torrent over two lines was one peer: one `anacrolix/torrent` client, one peer ID, for all of them. Each remote peer was reached over one line only (a second connection with the same peer ID is dropped by the library and, most likely, by the remote), all lines shared one list of known peers, trackers were announced through the first line, and the standing other peers give us (upload slots, tit-for-tat) was one peer's. On two lines (WLAN and 5G, limit 500 each) the torrent held 220–245 peers, with known ≈ connected: finding peers was the cap, not the limit. The spike behind this is in the ticket `.scratch/torrent-lines-as-peers`.

## Decision

- **One client per line** (`session.go`): its own peer ID, dialer, listeners, DHT and tracker dialer. The session is per VPN mode and line. With no usable line, a stand-in client with no socket carries the torrent, so it still checks its data.
- **One storage per torrent, shared** (`shared.go`). Two `TorrentImpl`s over the same files break on Windows: the one finishing the file renames `.part` while the other holds it open. A wrapper hands every line's client the same `TorrentImpl`, closed when the last client closes it. One piece completion.
- **A coordinator splits the pieces** (`lines.go`, `Torrent.balance`), only with two lines or more; one line keeps using the files' priorities. Each line is told to fetch its pieces (`Piece.SetPriority`, none for the others'). A piece goes to a line whose peers have it, preferably alive, in proportion to the lines' speeds; one already started stays. The last few pieces (`endgamePieces`) are asked for by every line, and the first copy wins.
- **A finished piece reaches every line** (`Piece.UpdateCompletion` on the others, which reads it from the shared storage; no hashing). If the storage still denies it a tick later, the line hashes the piece on disk: the storage can mark a file's pieces not complete when it is asked as the file is promoted from `.part`.
- **The info dict is fetched once**, by the main line; the others get it with `SetInfoBytes` and join once the first has checked the data on disk, so they hash nothing.
- **Private torrents run on the main line only.** A private tracker may count each peer ID or IP of an account as another user and flag it. Opting in to all lines per torrent is not built.
- **The card adds the lines up** (done, speeds, uploaded, peers, seeds, ratio); each line's peers and speed stay as the links.

## Consequences

- Peers see one DMS² per line. Seeding to a remote also happens once per line.
- Memory and goroutines grow by a client per line (measured at about 25 KiB and 2 goroutines idle, before peers).
- Not verified against libtorrent as the remote, with DHT or uTP per line, or under load.
