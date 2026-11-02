# Run WireGuard tunnels in userspace inside the engine

Date: 2026-11-02
Status: accepted

## Context

DMS² is getting a VPN so that torrent peers (and, by choice, HTTP servers) see a VPN address instead of the user's real one, while every interface still carries traffic. That means one WireGuard tunnel per physical interface, each one's encrypted UDP leaving through that interface only.

There are two ways to run WireGuard on Windows:

1. **Windows WireGuard adapters** (wireguard-nt). Each tunnel is a real network adapter that `netif.List` would find and the existing pinning would bind to.
2. **Userspace** (`wireguard-go` with a userspace TCP/IP stack such as gVisor netstack) inside `dms-engine.exe`. Tunnels are Go objects; nothing outside the engine sees them.

## Decision

Run tunnels in userspace inside the engine.

- The install stays per-user with no admin rights. Creating adapters needs admin, every time a tunnel is added.
- Each tunnel's outer UDP socket comes from its interface's existing `netif.Binding`, so pinning a tunnel to an interface needs no new mechanism.
- Windows routing never changes. Adapters would add routes that compete with the default route, and keeping them from taking over the whole machine's traffic is fiddly and fails open.
- The kill switch is structural: under VPN only, a torrent's dialers and listeners exist only on the tunnels' stacks, so there is no bare socket to leak through.

## Consequences

- Every protocol that must go through a tunnel needs the engine to hand it a dialer, listener or packet conn from the tunnel's stack: HTTP connections, torrent TCP and uTP peers, UDP trackers and DHT. Anything that opens its own sockets bypasses the tunnel, so third-party libraries must accept injected network functions (check this for `anacrolix/torrent` before relying on it).
- Resolving through a tunnel works because the per-interface SOCKS proxy dials through the same tunnel stack; Firefox containers need nothing new.
- Tunnel throughput is bounded by userspace packet handling. If that turns out to be the bottleneck on fast links, revisiting this means admin rights and route management.
- Other programs on the machine cannot use the tunnels. That is intended: DMS² is not a system VPN.
