package netif

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"downloadmanagersquared/engine/internal/rate"
)

// ipUnicastIf is IP_UNICAST_IF from ws2ipdef.h.
const ipUnicastIf = 31

// Binding sends traffic out through exactly one interface: sockets are bound to
// its address and pinned with IP_UNICAST_IF, and DNS goes to its own DNS servers.
// Every socket it hands out counts against the interface's shaper; its own DNS
// queries don't.
type Binding struct {
	iface    Interface
	shaper   *rate.Shaper
	resolver *net.Resolver
	client   *http.Client
	nextDNS  atomic.Uint32
}

func NewBinding(iface Interface, shaper *rate.Shaper) *Binding {
	b := &Binding{iface: iface, shaper: shaper}
	b.resolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			servers := b.iface.DNS
			if len(servers) == 0 {
				servers = []net.IP{{1, 1, 1, 1}, {8, 8, 8, 8}}
			}
			srv := servers[int(b.nextDNS.Add(1))%len(servers)]
			return b.dialRaw(ctx, network, net.JoinHostPort(srv.String(), "53"))
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

// DialContext resolves the host through this interface's DNS and connects over IPv4.
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
	ips, err := b.resolver.LookupIP(ctx, "ip4", host)
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
		lastErr = errors.New("no IPv4 address for " + host)
	}
	return nil, lastErr
}

// ListenPacket opens an IPv4 UDP socket on port (0: any free one) that sends
// through this interface.
func (b *Binding) ListenPacket(port int) (net.PacketConn, error) {
	lc := net.ListenConfig{Control: b.control}
	pc, err := lc.ListenPacket(context.Background(), "udp4", net.JoinHostPort(b.iface.IPv4.String(), strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	return b.shaper.PacketConn(pc), nil
}

// Listen takes TCP connections on this interface's address and port.
func (b *Binding) Listen(port int) (net.Listener, error) {
	lc := net.ListenConfig{Control: b.control}
	ln, err := lc.Listen(context.Background(), "tcp4", net.JoinHostPort(b.iface.IPv4.String(), strconv.Itoa(port)))
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

// LookupIP resolves host to IPv4 addresses through this interface's DNS.
func (b *Binding) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	return b.resolver.LookupIP(ctx, "ip4", host)
}

func (b *Binding) dialRaw(ctx context.Context, network, address string) (net.Conn, error) {
	d := net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second, Control: b.control}
	if len(network) >= 3 && network[:3] == "udp" {
		d.LocalAddr, network = &net.UDPAddr{IP: b.iface.IPv4}, "udp4"
	} else {
		d.LocalAddr, network = &net.TCPAddr{IP: b.iface.IPv4}, "tcp4"
	}
	return d.DialContext(ctx, network, address)
}

func (b *Binding) control(_, _ string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
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
