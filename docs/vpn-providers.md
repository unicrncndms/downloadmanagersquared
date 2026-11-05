# VPN providers and `.conf` files

DMS² runs its own WireGuard tunnels, one per connection (see [ADR 0001](adr/0001-userspace-wireguard-tunnels.md)). It doesn't use your VPN app. Instead, it needs a WireGuard config for each connection, and there are two ways to get one:

- **Import a `.conf`**: works with any provider that lets you download WireGuard configs. You need no account login in DMS², and DMS² never talks to the provider's servers except through the tunnel itself.
- **Sign in to Private Internet Access**: the one provider built in, because PIA doesn't hand out `.conf` files (see [below](#why-pia-is-the-odd-one-out)).

If your provider offers `.conf` files, use them. It's the simpler and more private route.

## What a `.conf` needs

- Plain WireGuard. Obfuscated variants such as AmneziaWG don't work, and neither do OpenVPN, IKEv2 or a provider's own protocol (Lightway, NordLynx through the app, …).
- An IPv4 `Address` and an IPv4 `DNS =` server. DMS² is IPv4-only, and it needs the DNS server to resolve names through the tunnel.
- **One config per connection.** Each must have its own key: at most providers that means a separate "device" in your account. Two tunnels with one key take each other's traffic, and DMS² refuses to import the same key twice.
- For sites that give each IP address its own download link, pick a **different server** for each connection, or they may end up sharing an exit address.

## Providers

Gathered in November 2026 from provider documentation and community reports. Providers change these things, so tell us if a row is wrong. "Not checked" means nobody has confirmed it yet.

| Provider | `.conf` from your account | Expires | Forwarded port for torrents |
|---|---|---|---|
| **Mullvad** | Yes | Not checked; believed to last until you revoke the device | No (Mullvad removed port forwarding in 2023) |
| **Proton VPN** | Yes: account.protonvpn.com → Downloads → WireGuard configuration | Reported as one year; make a new one before then. Logging out of a Proton app also revokes that app's keys | Yes, on P2P servers, if **NAT-PMP** is on when you make the config: DMS² picks it up by itself (not yet tested with Proton) |
| **IVPN** | Yes | Not checked | No (removed in 2023) |
| **AirVPN** | Yes: Client Area → Config Generator | Not checked | Fixed ports set in AirVPN's panel. Not checked for WireGuard configs |
| **Surfshark** | Yes (make a key in the dashboard) | Not checked | No |
| **Windscribe** | Yes, on paid plans (config generator) | Not checked | Reportedly not for manual WireGuard configs, only through Windscribe's app or OpenVPN |
| **Private Internet Access** | No: sign in instead | n/a | Yes, built in |
| **NordVPN** | No official `.conf` | n/a | No |
| **ExpressVPN** | Check: mostly its own Lightway protocol and OpenVPN | n/a | No |

Without a forwarded port a tunnel shows "outgoing only". Torrents still work: you connect to other peers, but peers behind their own NAT can't reach you, so popular torrents are fine and rare ones may be slower.

## Why PIA is the odd one out

Most providers make a config once, register its key on their servers, and let you download it. The file stays valid until you revoke it.

PIA has no such file. Its own scripts ([pia-foss/manual-connections](https://github.com/pia-foss/manual-connections)) work like this, and DMS² does the same:

1. Log in with your username and password to get a token. PIA documents it as valid for 24 hours.
2. Pick a WireGuard server in your region and register a fresh key with it, using the token.
3. The server returns your tunnel address for that key, and that makes the config.
4. The server forgets keys that aren't used for a while, and when it restarts. The config only lives as long as that registration. So DMS² registers a new key every time a tunnel starts rather than keeping a config.
5. Port forwarding goes through PIA's API too: a signature fetched inside the tunnel, valid for about two months, plus a "bind" every 15 minutes.

That's why PIA needs your login and every other provider doesn't. DMS² stores the PIA login encrypted for your Windows user (DPAPI, see [ADR 0005](adr/0005-secrets-at-rest-use-dpapi.md)), never shows it again, and only sends it to PIA. Picking **None** as the provider means PIA code never runs and nothing is stored.

## Port forwarding, by provider type

- **PIA**: automatic, in regions that support it.
- **NAT-PMP inside the tunnel** (Proton VPN): automatic. When an imported tunnel comes up, DMS² asks its gateway, through the tunnel only, whether it forwards ports. No login is needed. If the gateway answers, the tunnel gets a port, renewed about every 30 seconds, and shows "reachable". If not, it shows "outgoing only" after a few seconds and isn't asked again until it reconnects. Proton only forwards on P2P servers, with NAT-PMP turned on when you made the config, and not together with its "Moderate NAT" option. This is built from Proton's published guide and hasn't been tested against Proton's servers yet. If you use Proton, please [open an issue](https://github.com/unicrncndms/downloadmanagersquared/issues) saying whether your tunnel shows "reachable".
- **A fixed port from your provider's panel**: under consideration, if a provider is confirmed to forward one to WireGuard configs (AirVPN may). You'd type the port next to the imported `.conf`, and DMS² would show it as "forwarded (not checked)", since it can't verify it.

If a tunnel's config stops working after months, for example a Proton config past its expiry, the tunnel shows "the VPN server doesn't answer", with a hint that the config may have expired or been revoked. Make a new config at your provider and import it again.
