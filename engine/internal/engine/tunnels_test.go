package engine

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/netif"
	"downloadmanagersquared/engine/internal/tunnel"
)

// vpnServer is a WireGuard server on loopback serving a file at 10.9.0.1,
// an address only reachable through the tunnel.
type vpnServer struct {
	endpoint string
	pub      string
	data     []byte
}

func newVPNServer(t *testing.T, clientPub string) *vpnServer {
	t.Helper()
	priv, pub, err := tunnel.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := tunnel.ParseConf(fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.9.0.1/24\n[Peer]\nPublicKey = %s\nAllowedIPs = 10.9.0.2/32\n", priv, clientPub))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := tunnel.Start(cfg, func() (net.PacketConn, error) { return net.ListenPacket("udp4", "127.0.0.1:0") }, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	data := make([]byte, 3<<20)
	rand.Read(data)
	ln, err := srv.Listen(80)
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="vpn.bin"`)
		http.ServeContent(w, r, "vpn.bin", time.Time{}, bytes.NewReader(data))
	}))
	return &vpnServer{endpoint: srv.LocalAddr().String(), pub: pub, data: data}
}

// loopbackEngine is an engine whose only interface is this machine's loopback.
func loopbackEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Shutdown)
	e.settings.SaveDir = t.TempDir()
	ifs, _ := net.Interfaces()
	var idx uint32
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 {
			idx = uint32(i.Index)
		}
	}
	e.mu.Lock()
	e.refreshInterfacesLocked([]netif.Interface{{ID: "lo", Name: "Loopback", Index: idx, IPv4: net.IPv4(127, 0, 0, 1)}})
	e.mu.Unlock()
	return e
}

func importConf(t *testing.T, e *Engine, srv *vpnServer, clientPriv string) {
	t.Helper()
	conf := fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.9.0.2/32\n[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = %s\nPersistentKeepalive = 25\n",
		clientPriv, srv.pub, srv.endpoint)
	p, _ := json.Marshal(map[string]string{"iface": "lo", "conf": conf})
	if _, err := e.Call(nil, "importTunnel", p); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPUnderVPNOnlyWaitsForATunnelThenUsesOnlyIt(t *testing.T) {
	e := loopbackEngine(t)
	clientPriv, clientPub, _ := tunnel.GenerateKey()
	srv := newVPNServer(t, clientPub)

	id, err := e.Add(AddRequest{URL: "http://10.9.0.1/vpn.bin", VPN: "vpn"})
	if err != nil {
		t.Fatal(err)
	}
	d, _ := e.get(id)
	e.schedule()
	waitFor(t, "the download to pause without a tunnel", 10*time.Second, d.WaitingForVPN)

	importConf(t, e, srv, clientPriv)
	waitFor(t, "the download over the tunnel", 60*time.Second, func() bool {
		e.syncTunnels(time.Now())
		e.schedule()
		return d.Status() == download.StatusCompleted
	})
	got, err := os.ReadFile(filepath.Join(e.settings.SaveDir, "vpn.bin"))
	if err != nil || !bytes.Equal(got, srv.data) {
		t.Fatalf("file over the tunnel: %d bytes, %v", len(got), err)
	}
	for path := range d.Summary().Links {
		if path != tunnelPath("lo") {
			t.Fatalf("under VPN only the download used %q", path)
		}
	}
}

func TestTunnelGetsItsOwnProxyForResolving(t *testing.T) {
	e := loopbackEngine(t)
	clientPriv, clientPub, _ := tunnel.GenerateKey()
	srv := newVPNServer(t, clientPub)
	importConf(t, e, srv, clientPriv)
	waitFor(t, "the tunnel to come up", 30*time.Second, func() bool {
		e.syncTunnels(time.Now())
		for _, u := range e.Usable(download.VPNOnly) {
			if u.ID == tunnelPath("lo") {
				return true
			}
		}
		return false
	})
	if bare := e.Usable(download.VPNOff); len(bare) != 1 || bare[0].ID != "lo" {
		t.Fatalf("with VPN off the paths are %v, want the bare interface", bare)
	}
	if all := e.Usable(download.VPNAlongside); len(all) != 2 {
		t.Fatalf("alongside, the paths are %v, want the interface and its tunnel", all)
	}

	v := ifaceView(t, e, tunnelPath("lo"))
	if !v.Tunnel || !v.Present || v.Name != "VPN via Loopback" || v.SocksPort == 0 {
		t.Fatalf("tunnel shown as %+v", v)
	}
	// The browser resolves landing pages through this proxy: it must reach what the tunnel reaches.
	proxy, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", v.SocksPort))
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}, Timeout: 20 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, "http://10.9.0.1/vpn.bin", nil)
	req.Header.Set("Range", "bytes=0-9")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(b, srv.data[:10]) {
		t.Fatalf("through the tunnel's proxy: %q", b)
	}
}

func TestTunnelKeysStayOutOfTheSettings(t *testing.T) {
	e := loopbackEngine(t)
	clientPriv, clientPub, _ := tunnel.GenerateKey()
	importConf(t, e, newVPNServer(t, clientPub), clientPriv)
	s, _ := e.Call(nil, "getSettings", nil)
	b, _ := json.Marshal(s)
	h, _ := json.Marshal(e.Snapshot())
	if bytes.Contains(b, []byte(clientPriv)) || bytes.Contains(h, []byte(clientPriv)) {
		t.Fatal("the tunnel's private key reaches the extension")
	}
}
