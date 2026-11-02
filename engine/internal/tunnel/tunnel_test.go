package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

func listenLoopback(sent *atomic.Int64) func() (net.PacketConn, error) {
	return func() (net.PacketConn, error) {
		pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
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

[Peer]
PublicKey = %s
AllowedIPs = 10.9.0.2/32
`, serverKey, clientPub))
	if err != nil {
		t.Fatal(err)
	}
	server, err = Start(srvCfg, listenLoopback(&serverSent), nil)
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
	client, err = Start(cliCfg, listenLoopback(sent), nil)
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
	cfg, err := ParseConf(fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.9.0.2/32\n[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = %s\n", key, pub, pc.LocalAddr()))
	if err != nil {
		t.Fatal(err)
	}
	var sent atomic.Int64
	tun, err := Start(cfg, listenLoopback(&sent), nil)
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
