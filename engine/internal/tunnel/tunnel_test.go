package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"
)

// countingPC counts the datagrams a tunnel sends through the socket it was given.
type countingPC struct {
	net.PacketConn
	sent *atomic.Int64
}

func (c countingPC) WriteTo(p []byte, a net.Addr) (int, error) {
	c.sent.Add(1)
	return c.PacketConn.WriteTo(p, a)
}

func listenLoopback(sent *atomic.Int64) func(v6 bool) (net.PacketConn, error) {
	return func(v6 bool) (net.PacketConn, error) {
		network, addr := "udp4", "127.0.0.1:0"
		if v6 {
			network, addr = "udp6", "[::1]:0"
		}
		pc, err := net.ListenPacket(network, addr)
		if err != nil {
			return nil, err
		}
		return countingPC{pc, sent}, nil
	}
}

// pair starts a VPN "server" and a client tunnel to it, both in this process.
func pair(t *testing.T) (server, client *Tunnel, sent *atomic.Int64) {
	t.Helper()
	serverKey, serverPub := mustKey(t)
	clientKey, clientPub := mustKey(t)

	// The server's endpoint is only known once its socket is open, so start it first.
	var serverSent atomic.Int64
	srvCfg, err := ParseConf(fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = 10.9.0.1/24
DNS = 10.9.0.1

[Peer]
PublicKey = %s
AllowedIPs = 10.9.0.2/32
`, serverKey, clientPub))
	if err != nil {
		t.Fatal(err)
	}
	server, err = Start(context.Background(), srvCfg, listenLoopback(&serverSent), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)

	sent = &atomic.Int64{}
	cliCfg, err := ParseConf(fmt.Sprintf(`# imported from a provider
[Interface]
PrivateKey = %s
Address = 10.9.0.2/32, fd00::2/128
DNS = 10.9.0.1

[Peer]
PublicKey = %s
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = %s
PersistentKeepalive = 25
`, clientKey, serverPub, server.LocalAddr()))
	if err != nil {
		t.Fatal(err)
	}
	client, err = Start(context.Background(), cliCfg, listenLoopback(sent), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return server, client, sent
}

func mustKey(t *testing.T) (priv, pub string) {
	t.Helper()
	priv, pub, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

func TestHTTPGoesThroughTheTunnel(t *testing.T) {
	server, client, sent := pair(t)
	ln, err := server.Listen(80)
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello %s", strings.Split(r.RemoteAddr, ":")[0])
	}))

	c := &http.Client{Transport: &http.Transport{DialContext: client.DialContext}, Timeout: 10 * time.Second}
	resp, err := c.Get("http://10.9.0.1/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// The server sees the client's tunnel address, not its real one.
	if string(body) != "hello 10.9.0.2" {
		t.Fatalf("got %q", body)
	}
	if sent.Load() == 0 {
		t.Fatal("nothing went out through the socket the tunnel was given")
	}
	if !client.Up() {
		t.Fatal("a tunnel that just carried traffic isn't up")
	}
}

func TestUDPGoesThroughTheTunnel(t *testing.T) {
	server, client, _ := pair(t)
	srv, err := server.ListenPacket(6881)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 100)
		for {
			n, from, err := srv.ReadFrom(buf)
			if err != nil {
				return
			}
			srv.WriteTo(buf[:n], from)
		}
	}()
	pc, err := client.ListenPacket(0)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	pc.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 100)
	for {
		pc.WriteTo([]byte("ping"), &net.UDPAddr{IP: net.IPv4(10, 9, 0, 1), Port: 6881})
		pc.SetReadDeadline(time.Now().Add(time.Second))
		n, _, err := pc.ReadFrom(buf)
		if err == nil && string(buf[:n]) == "ping" {
			return
		}
		if ctxDone(t) {
			t.Fatal("no echo through the tunnel")
		}
	}
}

var udpDeadline = time.Now().Add(15 * time.Second)

func ctxDone(t *testing.T) bool { return time.Now().After(udpDeadline) }

func TestTunnelWithoutAServerIsDown(t *testing.T) {
	key, _ := mustKey(t)
	_, pub := mustKey(t)
	pc, _ := net.ListenPacket("udp4", "127.0.0.1:0") // nothing answers here
	defer pc.Close()
	cfg, err := ParseConf(fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.9.0.2/32\nDNS = 10.9.0.1\n[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = %s\n", key, pub, pc.LocalAddr()))
	if err != nil {
		t.Fatal(err)
	}
	var sent atomic.Int64
	tun, err := Start(context.Background(), cfg, listenLoopback(&sent), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if c, err := tun.DialContext(ctx, "tcp", "10.9.0.1:80"); err == nil {
		c.Close()
		t.Fatal("dialed through a tunnel with no server")
	}
	if tun.Up() {
		t.Fatal("a tunnel that never shook hands is up")
	}
}

func TestBadConfIsRefused(t *testing.T) {
	for _, conf := range []string{
		"",
		"[Interface]\nPrivateKey = nope\nAddress = 10.0.0.2/32\n[Peer]\nPublicKey = x\nEndpoint = 1.2.3.4:51820\n",
		"[Interface]\nAddress = 10.0.0.2/32\n", // no key, no peer
	} {
		if _, err := ParseConf(conf); err == nil {
			t.Errorf("accepted %q", conf)
		}
	}
}

func TestAnMTUOutsideItsRangeIsRefused(t *testing.T) {
	key, _, _ := GenerateKey()
	_, pub, _ := GenerateKey()
	for _, mtu := range []string{"0", "-5", "70000"} {
		conf := fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.8.0.2/32\nDNS = 10.8.0.1\nMTU = %s\n[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = 1.2.3.4:51820\n", key, mtu, pub)
		if _, err := ParseConf(conf); err == nil {
			t.Errorf("MTU %s was accepted", mtu)
		}
	}
}

func TestAConfLineWithoutEqualsDoesNotEchoItsKey(t *testing.T) {
	key, _, _ := GenerateKey()
	key = strings.TrimRight(key, "=") // the padding is an "=", which would split the line
	_, err := ParseConf("[Interface]\nPrivateKey " + key + "\n")
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("got %v, want an error naming line 2", err)
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("the error holds the private key: %v", err)
	}
}

func TestAmneziaWGConfIsRefusedSayingWhy(t *testing.T) {
	key, _, _ := GenerateKey()
	_, pub, _ := GenerateKey()
	conf := fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.8.0.2/32\nJc = 4\nJmin = 40\nJmax = 70\nS1 = 0\nS2 = 0\nH1 = 1\nH2 = 2\nH3 = 3\nH4 = 4\n[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = 1.2.3.4:51820\n", key, pub)
	_, err := ParseConf(conf)
	if err == nil || !strings.Contains(err.Error(), "AmneziaWG") {
		t.Fatalf("got %v, want a refusal naming AmneziaWG", err)
	}
}

func TestConfWithoutIPv4DNSIsRefusedSayingWhy(t *testing.T) {
	key, _, _ := GenerateKey()
	_, pub, _ := GenerateKey()
	for name, dns := range map[string]string{
		"no DNS":               "",
		"only IPv6 DNS":        "DNS = 2606:4700:4700::1111, fd00::1\n",
		"only a search domain": "DNS = vpn.example\n",
	} {
		conf := fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.8.0.2/32\n%s[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = 1.2.3.4:51820\n", key, dns, pub)
		_, err := ParseConf(conf)
		if !errors.Is(err, ErrNoDNS) {
			t.Errorf("%s: got %v, want ErrNoDNS", name, err)
			continue
		}
		if msg := err.Error(); !strings.Contains(msg, "DNS") || !strings.Contains(msg, "IPv6") {
			t.Errorf("%s: %q doesn't say an IPv4 DNS server is needed", name, msg)
		}
	}
}

func TestTunnelWithoutDNSDoesntStart(t *testing.T) {
	key, _, _ := GenerateKey()
	_, pub, _ := GenerateKey()
	cfg, _ := ParseConf(fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.8.0.2/32\nDNS = 10.8.0.1\n[Peer]\nPublicKey = %s\nEndpoint = 127.0.0.1:9\n", key, pub))
	cfg.DNS = nil
	if tn, err := Start(context.Background(), cfg, listenLoopback(&atomic.Int64{}), nil); !errors.Is(err, ErrNoDNS) {
		if tn != nil {
			tn.Close()
		}
		t.Fatalf("got %v, want ErrNoDNS", err)
	}
}

// benchBind opens a packetBind on a loopback socket and returns it with that
// socket's address and receive funcs. The socket is closed with the benchmark.
func benchBind(b *testing.B) (*packetBind, *net.UDPAddr, []conn.ReceiveFunc) {
	b.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	bind := &packetBind{listen: func() (net.PacketConn, error) { return pc, nil }}
	fns, _, err := bind.Open(0)
	if err != nil {
		pc.Close()
		b.Fatal(err)
	}
	b.Cleanup(func() { bind.Close() })
	return bind, pc.LocalAddr().(*net.UDPAddr), fns
}

// BenchmarkReceive is one datagram in and out of WireGuard's receive func.
func BenchmarkReceive(b *testing.B) {
	_, addr, fns := benchBind(b)
	peer, err := net.DialUDP("udp4", nil, addr) // connected, so Write takes no address to allocate
	if err != nil {
		b.Fatal(err)
	}
	defer peer.Close()
	payload := make([]byte, 1400)
	packets := [][]byte{make([]byte, 1500)}
	sizes := make([]int, 1)
	eps := make([]conn.Endpoint, 1)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := peer.Write(payload); err != nil {
			b.Fatal(err)
		}
		if _, err := fns[0](packets, sizes, eps); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSend is one datagram through WireGuard's Send. Nothing reads the
// far end, so its socket just fills and drops; only the send is measured.
func BenchmarkSend(b *testing.B) {
	bind, _, _ := benchBind(b)
	sink, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer sink.Close()
	ep, err := bind.ParseEndpoint(sink.LocalAddr().String())
	if err != nil {
		b.Fatal(err)
	}
	bufs := [][]byte{make([]byte, 1400)}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := bind.Send(bufs, ep); err != nil {
			b.Fatal(err)
		}
	}
}
