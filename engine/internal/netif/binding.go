package netif

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"log"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"downloadmanagersquared/engine/internal/rate"
)

// ipUnicastIf is IP_UNICAST_IF from ws2ipdef.h, and IPV6_UNICAST_IF too.
const ipUnicastIf = 31

// Binding sends traffic out through exactly one interface: sockets are bound to
// its address and pinned with IP_UNICAST_IF (IPV6_UNICAST_IF), and DNS goes to
// its own DNS servers. It uses only the families the interface has, so IPv6
// only when List gave it an IPv6 address, which needs IPv6 switched on.
// Every socket it hands out counts against the interface's shaper; its own DNS
// queries don't.
type Binding struct {
	iface    Interface
	shaper   *rate.Shaper
	resolver *net.Resolver
	client   *http.Client
	nextDNS  atomic.Uint32
	dns      []net.IP // the servers asked: the interface's, else public ones
}

// dnsPort is where DNS servers are asked; tests point it elsewhere.
var dnsPort = "53"

// Public resolvers for an interface that names none. An IPv6-only interface
// loses its carrier's DNS64 with them: IPv4-only servers can't be reached.
var (
	fallbackDNS4 = []net.IP{{1, 1, 1, 1}, {8, 8, 8, 8}}
	fallbackDNS6 = []net.IP{net.ParseIP("2606:4700:4700::1111"), net.ParseIP("2001:4860:4860::8888")}
)

func NewBinding(iface Interface, shaper *rate.Shaper) *Binding {
	b := &Binding{iface: iface, shaper: shaper, dns: iface.DNS}
	if len(b.dns) == 0 {
		if b.dns = fallbackDNS4; iface.IPv4 == nil {
			b.dns = fallbackDNS6
			log.Printf("%s has only IPv6 and no DNS server: asking %v, without the carrier's DNS64", iface.Name, b.dns)
		}
	}
	b.resolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			srv := b.dns[int(b.nextDNS.Add(1))%len(b.dns)]
			host := srv.String()
			if srv.To4() == nil && srv.IsLinkLocalUnicast() {
				host += "%" + strconv.Itoa(int(b.iface.Index)) // a router's fe80:: address, on this link
			}
			return b.dialRaw(ctx, network, net.JoinHostPort(host, dnsPort))
		},
	}
	b.client = NewHTTPClient(b.DialContext)
	return b
}

// NewHTTPClient is a client for downloads whose connections come from dial.
func NewHTTPClient(dial func(ctx context.Context, network, addr string) (net.Conn, error)) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: dial,
			// Force HTTP/1.1: with HTTP/2 every range request would share one TCP connection.
			TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
			DisableCompression:    true,
			MaxIdleConnsPerHost:   64,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   20 * time.Second,
			ResponseHeaderTimeout: 45 * time.Second,
		},
	}
}

// HTTPClient returns a client whose connections all leave through this interface.
func (b *Binding) HTTPClient() *http.Client { return b.client }

func (b *Binding) Close() { b.client.CloseIdleConnections() }

// Has4 and Has6 are the families the interface is used with.
func (b *Binding) Has4() bool { return b.iface.IPv4 != nil }
func (b *Binding) Has6() bool { return b.iface.IPv6 != nil }

// DialContext resolves the host through this interface's DNS and connects over
// the families the interface has, trying IPv4 addresses first.
func (b *Binding) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		c, err := b.dialRaw(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return b.shaper.Conn(c), nil
	}
	ips, err := b.LookupIP(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, ip := range ips {
		c, err := b.dialRaw(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return b.shaper.Conn(c), nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no address for " + host)
	}
	return nil, lastErr
}

// ListenPacket opens an IPv4 UDP socket on port (0: any free one) that sends
// through this interface.
func (b *Binding) ListenPacket(port int) (net.PacketConn, error) { return b.listenPacket("udp4", port) }

// ListenPacket6 is ListenPacket over IPv6.
func (b *Binding) ListenPacket6(port int) (net.PacketConn, error) { return b.listenPacket("udp6", port) }

func (b *Binding) listenPacket(network string, port int) (net.PacketConn, error) {
	ip, err := b.local(network)
	if err != nil {
		return nil, err
	}
	lc := net.ListenConfig{Control: b.control}
	pc, err := lc.ListenPacket(context.Background(), network, net.JoinHostPort(ip.String(), strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	return b.shaper.PacketConn(pc), nil
}

// Listen takes TCP connections on this interface's IPv4 address and port.
func (b *Binding) Listen(port int) (net.Listener, error) { return b.listen("tcp4", port) }

// Listen6 is Listen on the interface's IPv6 address.
func (b *Binding) Listen6(port int) (net.Listener, error) { return b.listen("tcp6", port) }

func (b *Binding) listen(network string, port int) (net.Listener, error) {
	ip, err := b.local(network)
	if err != nil {
		return nil, err
	}
	lc := net.ListenConfig{Control: b.control}
	ln, err := lc.Listen(context.Background(), network, net.JoinHostPort(ip.String(), strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	return shapedListener{ln, b.shaper}, nil
}

type shapedListener struct {
	net.Listener
	s *rate.Shaper
}

func (l shapedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return l.s.Conn(c), nil
}

// An address of a family the interface doesn't have.
var (
	// shortcut: an IPv6-only connection reaches IPv4 servers only by name,
	// through its carrier's DNS64, so a literal IPv4 address fails here; RFC
	// 7050 NAT64 prefix discovery would map it, if such links turn up.
	ErrNoIPv4 = errors.New("this connection has no IPv4")
	ErrNoIPv6 = errors.New("this connection has no IPv6")
)

// local is the interface's address for network ("tcp4", "udp6", ...).
func (b *Binding) local(network string) (net.IP, error) {
	if strings.HasSuffix(network, "6") {
		if b.iface.IPv6 == nil {
			return nil, ErrNoIPv6
		}
		return b.iface.IPv6, nil
	}
	if b.iface.IPv4 == nil {
		return nil, ErrNoIPv4
	}
	return b.iface.IPv4, nil
}

// LookupIP resolves host through this interface's DNS, asking only for the
// families it has (A, AAAA or both), IPv4 addresses first.
func (b *Binding) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	network := "ip4"
	switch {
	case b.iface.IPv4 == nil && b.iface.IPv6 != nil:
		network = "ip6"
	case b.iface.IPv6 != nil:
		network = "ip"
	}
	ips, err := b.resolver.LookupIP(ctx, network, host)
	slices.SortStableFunc(ips, func(x, y net.IP) int { return len(y.To4()) - len(x.To4()) })
	return ips, err
}

// dialRaw connects to address, an IP and port, from this interface's address of
// that family.
func (b *Binding) dialRaw(ctx context.Context, network, address string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	host, zone, _ := strings.Cut(host, "%")
	ip := net.ParseIP(host)
	if ip == nil {
		return nil, errors.New("not an IP address: " + host)
	}
	network = network[:3] + "4"
	if ip.To4() == nil {
		network = network[:3] + "6"
	}
	local, err := b.local(network)
	if err != nil {
		return nil, err
	}
	if zone != "" {
		local = nil // a link-local peer: IPV6_UNICAST_IF picks the link, Windows the source
	}
	d := net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second, Control: b.control}
	if network[:3] == "udp" {
		d.LocalAddr = &net.UDPAddr{IP: local}
	} else {
		d.LocalAddr = &net.TCPAddr{IP: local}
	}
	return d.DialContext(ctx, network, address)
}

func (b *Binding) control(network, _ string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		if strings.HasSuffix(network, "6") {
			// IPV6_UNICAST_IF takes the index in host byte order.
			serr = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IPV6, ipUnicastIf, int(b.iface.Index))
			return
		}
		// IPv4 IP_UNICAST_IF expects the index in network byte order.
		var be [4]byte
		binary.BigEndian.PutUint32(be[:], b.iface.Index)
		serr = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, ipUnicastIf, int(int32(binary.LittleEndian.Uint32(be[:]))))
	})
	if err != nil {
		return err
	}
	return serr
}
