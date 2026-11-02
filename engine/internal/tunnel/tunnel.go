// Package tunnel runs a WireGuard tunnel in userspace (wireguard-go on a gVisor
// network stack). Its encrypted UDP goes out through a socket the caller
// supplies, so a tunnel is carried by exactly one interface; what goes inside
// it comes from the tunnel's own Dial, Listen and ListenPacket.
package tunnel

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// Config is a WireGuard config with one peer, IPv4 only.
type Config struct {
	PrivateKey   [32]byte
	Address      netip.Addr   // the tunnel's address
	DNS          []netip.Addr // resolvers inside the tunnel
	MTU          int
	PublicKey    [32]byte // the peer's
	PresharedKey *[32]byte
	Endpoint     string // host:port, empty for a peer that connects to us
	AllowedIPs   []netip.Prefix
	Keepalive    int // seconds, 0 = none
}

// ParseConf reads a WireGuard .conf file. IPv6 addresses are left out.
func ParseConf(text string) (Config, error) {
	cfg := Config{MTU: 1420}
	var section string
	var haveKey, havePeer, haveAddr bool
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.IndexAny(line, "#;"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.ToLower(strings.Trim(line, "[] "))
			if section == "peer" {
				if havePeer {
					return Config{}, errors.New("only one [Peer] is supported")
				}
				havePeer = true
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return Config{}, fmt.Errorf("can't read %q", line)
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		var err error
		switch section + "." + k {
		case "interface.privatekey":
			cfg.PrivateKey, err = parseKey(v)
			haveKey = err == nil
		case "interface.address":
			for _, a := range list(v) {
				p, perr := netip.ParsePrefix(a)
				if perr != nil {
					addr, aerr := netip.ParseAddr(a)
					if aerr != nil {
						return Config{}, fmt.Errorf("address %q: %w", a, perr)
					}
					p = netip.PrefixFrom(addr, addr.BitLen())
				}
				if p.Addr().Is4() && !haveAddr {
					cfg.Address, haveAddr = p.Addr(), true
				}
			}
		case "interface.dns":
			for _, a := range list(v) {
				if addr, err := netip.ParseAddr(a); err == nil && addr.Is4() {
					cfg.DNS = append(cfg.DNS, addr)
				} // search domains and IPv6 resolvers are ignored
			}
		case "interface.mtu":
			cfg.MTU, err = strconv.Atoi(v)
		case "peer.publickey":
			cfg.PublicKey, err = parseKey(v)
		case "peer.presharedkey":
			var psk [32]byte
			psk, err = parseKey(v)
			cfg.PresharedKey = &psk
		case "peer.endpoint":
			if _, _, err = net.SplitHostPort(v); err == nil {
				cfg.Endpoint = v
			}
		case "peer.allowedips":
			for _, a := range list(v) {
				p, perr := netip.ParsePrefix(a)
				if perr != nil {
					return Config{}, fmt.Errorf("allowed IPs %q: %w", a, perr)
				}
				if p.Addr().Is4() {
					cfg.AllowedIPs = append(cfg.AllowedIPs, p)
				}
			}
		case "peer.persistentkeepalive":
			if v != "off" {
				cfg.Keepalive, err = strconv.Atoi(v)
			}
		}
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", k, err)
		}
	}
	switch {
	case !haveKey:
		return Config{}, errors.New("no PrivateKey")
	case !haveAddr:
		return Config{}, errors.New("no IPv4 Address")
	case !havePeer || cfg.PublicKey == [32]byte{}:
		return Config{}, errors.New("no [Peer] with a PublicKey")
	}
	return cfg, nil
}

func list(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func parseKey(s string) ([32]byte, error) {
	var k [32]byte
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 32 {
		return k, errors.New("not a WireGuard key")
	}
	copy(k[:], b)
	return k, nil
}

// GenerateKey makes a WireGuard key pair, base64 as in a .conf file.
func GenerateKey() (priv, pub string, err error) {
	var k [32]byte
	if _, err := rand.Read(k[:]); err != nil {
		return "", "", err
	}
	k[0] &= 248
	k[31] = (k[31] & 127) | 64
	p, err := curve25519.X25519(k[:], curve25519.Basepoint)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(k[:]), base64.StdEncoding.EncodeToString(p), nil
}

// Tunnel is one running WireGuard tunnel.
type Tunnel struct {
	dev  *device.Device
	tnet *netstack.Net
	cfg  Config
	bind *packetBind
}

// Start brings a tunnel up. listen opens the UDP socket its encrypted packets
// go through; lookup resolves the endpoint's host name, outside the tunnel
// (nil if the endpoint is an address).
func Start(cfg Config, listen func() (net.PacketConn, error), lookup func(ctx context.Context, host string) ([]net.IP, error)) (*Tunnel, error) {
	endpoint := ""
	if cfg.Endpoint != "" {
		host, port, _ := net.SplitHostPort(cfg.Endpoint)
		ip, err := netip.ParseAddr(host)
		if err != nil {
			if lookup == nil {
				return nil, fmt.Errorf("can't resolve the endpoint %s", host)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			ips, lerr := lookup(ctx, host)
			cancel()
			if lerr != nil || len(ips) == 0 {
				return nil, fmt.Errorf("resolving the endpoint %s: %v", host, lerr)
			}
			ip, _ = netip.AddrFromSlice(ips[0].To4())
		}
		endpoint = net.JoinHostPort(ip.String(), port)
	}
	tdev, tnet, err := netstack.CreateNetTUN([]netip.Addr{cfg.Address}, cfg.DNS, cfg.MTU)
	if err != nil {
		return nil, err
	}
	bind := &packetBind{listen: listen}
	dev := device.NewDevice(tdev, bind, device.NewLogger(device.LogLevelSilent, ""))
	var uapi strings.Builder
	fmt.Fprintf(&uapi, "private_key=%s\npublic_key=%s\n", hex.EncodeToString(cfg.PrivateKey[:]), hex.EncodeToString(cfg.PublicKey[:]))
	if cfg.PresharedKey != nil {
		fmt.Fprintf(&uapi, "preshared_key=%s\n", hex.EncodeToString(cfg.PresharedKey[:]))
	}
	if endpoint != "" {
		fmt.Fprintf(&uapi, "endpoint=%s\n", endpoint)
	}
	if cfg.Keepalive > 0 {
		fmt.Fprintf(&uapi, "persistent_keepalive_interval=%d\n", cfg.Keepalive)
	}
	for _, p := range cfg.AllowedIPs {
		fmt.Fprintf(&uapi, "allowed_ip=%s\n", p)
	}
	if err := dev.IpcSet(uapi.String()); err != nil {
		dev.Close()
		return nil, err
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, err
	}
	return &Tunnel{dev: dev, tnet: tnet, cfg: cfg, bind: bind}, nil
}

// Close takes the tunnel down.
func (t *Tunnel) Close() { t.dev.Close() }

// LocalAddr is the outer socket's address.
func (t *Tunnel) LocalAddr() net.Addr { return t.bind.localAddr() }

// Address is the tunnel's own address, which the other side sees.
func (t *Tunnel) Address() net.IP { return net.IP(t.cfg.Address.AsSlice()) }

// DialContext connects through the tunnel, resolving names with its DNS.
func (t *Tunnel) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4":
		network = "tcp4"
	case "udp", "udp4":
		network = "udp4"
	default:
		return nil, fmt.Errorf("tunnel: no %s", network)
	}
	return t.tnet.DialContext(ctx, network, addr)
}

// LookupIP resolves host to IPv4 addresses with the tunnel's DNS.
func (t *Tunnel) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	addrs, err := t.tnet.LookupContextHost(ctx, host)
	if err != nil {
		return nil, err
	}
	var out []net.IP
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
			out = append(out, ip)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no IPv4 address for " + host)
	}
	return out, nil
}

// ListenPacket opens a UDP socket inside the tunnel on port (0: any).
func (t *Tunnel) ListenPacket(port int) (net.PacketConn, error) {
	return t.tnet.ListenUDP(&net.UDPAddr{IP: t.Address(), Port: port})
}

// Listen takes TCP connections inside the tunnel on port.
func (t *Tunnel) Listen(port int) (net.Listener, error) {
	return t.tnet.ListenTCP(&net.TCPAddr{IP: t.Address(), Port: port})
}

// upWithin is how recent a handshake must be for the tunnel to count as up.
// WireGuard shakes hands again every two minutes while packets flow, and a
// keepalive keeps them flowing.
const upWithin = 3*time.Minute + 20*time.Second

// Up is whether the tunnel shook hands with its server lately.
func (t *Tunnel) Up() bool {
	s, err := t.dev.IpcGet()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(line, "last_handshake_time_sec="); ok {
			sec, _ := strconv.ParseInt(v, 10, 64)
			return sec > 0 && time.Since(time.Unix(sec, 0)) < upWithin
		}
	}
	return false
}

// AddPeer lets one more peer in, for a tunnel that is the server side.
func (t *Tunnel) AddPeer(pub string, allowed netip.Prefix) error {
	k, err := parseKey(pub)
	if err != nil {
		return err
	}
	return t.dev.IpcSet(fmt.Sprintf("public_key=%s\nreplace_allowed_ips=true\nallowed_ip=%s\n", hex.EncodeToString(k[:]), allowed))
}

// Shake makes the tunnel shake hands now, so Up soon tells whether its server answers.
func (t *Tunnel) Shake() {
	c, err := t.tnet.DialUDP(nil, &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 9})
	if err != nil {
		return
	}
	c.Write([]byte{0}) // to TEST-NET: never answered, but it starts the handshake
	c.Close()
}

// packetBind carries WireGuard's UDP over one socket the caller opens.
type packetBind struct {
	listen func() (net.PacketConn, error)
	mu     sync.Mutex
	pc     net.PacketConn
}

type endpoint netip.AddrPort

func (e endpoint) ClearSrc()           {}
func (e endpoint) SrcToString() string { return "" }
func (e endpoint) DstToString() string { return netip.AddrPort(e).String() }
func (e endpoint) DstToBytes() []byte {
	b, _ := netip.AddrPort(e).MarshalBinary()
	return b
}
func (e endpoint) DstIP() netip.Addr { return netip.AddrPort(e).Addr() }
func (e endpoint) SrcIP() netip.Addr { return netip.Addr{} }

func (b *packetBind) Open(uint16) ([]conn.ReceiveFunc, uint16, error) {
	pc, err := b.listen()
	if err != nil {
		return nil, 0, err
	}
	b.mu.Lock()
	b.pc = pc
	b.mu.Unlock()
	recv := func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		for {
			n, from, err := pc.ReadFrom(packets[0])
			if err != nil {
				return 0, err
			}
			ap, perr := netip.ParseAddrPort(from.String())
			if perr != nil {
				continue
			}
			sizes[0], eps[0] = n, endpoint(netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()))
			return 1, nil
		}
	}
	port := 0
	if a, ok := pc.LocalAddr().(*net.UDPAddr); ok {
		port = a.Port
	}
	return []conn.ReceiveFunc{recv}, uint16(port), nil
}

func (b *packetBind) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pc == nil {
		return nil
	}
	err := b.pc.Close()
	b.pc = nil
	return err
}

func (b *packetBind) localAddr() net.Addr {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pc == nil {
		return nil
	}
	return b.pc.LocalAddr()
}

func (b *packetBind) SetMark(uint32) error { return nil }
func (b *packetBind) BatchSize() int       { return 1 }

func (b *packetBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		return nil, err
	}
	return endpoint(ap), nil
}

func (b *packetBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	b.mu.Lock()
	pc := b.pc
	b.mu.Unlock()
	if pc == nil {
		return net.ErrClosed
	}
	to := net.UDPAddrFromAddrPort(netip.AddrPort(ep.(endpoint)))
	for _, buf := range bufs {
		if _, err := pc.WriteTo(buf, to); err != nil {
			return err
		}
	}
	return nil
}
