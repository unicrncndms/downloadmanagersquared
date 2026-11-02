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
	t        *tunnel.Tunnel
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
	return &vpnServer{t: srv, endpoint: srv.LocalAddr().String(), pub: pub, data: data}
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
	e.mu.Lock()
	e.refreshInterfacesLocked(loopbackIfaces())
	e.mu.Unlock()
	return e
}

func loopbackIfaces() []netif.Interface {
	ifs, _ := net.Interfaces()
	var idx uint32
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 {
			idx = uint32(i.Index)
		}
	}
	return []netif.Interface{{ID: "lo", Name: "Loopback", Index: idx, IPv4: net.IPv4(127, 0, 0, 1)}}
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

func waitTunnelUp(t *testing.T, e *Engine) {
	t.Helper()
	waitFor(t, "the tunnel to come up", 30*time.Second, func() bool {
		e.syncTunnels(time.Now())
		return e.anyTunnelUp()
	})
}

func TestVPNOnlyTorrentsGetNoBareSocket(t *testing.T) {
	e := loopbackEngine(t)
	clientPriv, clientPub, _ := tunnel.GenerateKey()
	srv := newVPNServer(t, clientPub)
	importConf(t, e, srv, clientPriv)
	waitTunnelUp(t, e)

	ids := func(m download.VPNMode) []string {
		var out []string
		for _, i := range e.torrentIfacesFor(m) {
			out = append(out, i.ID)
		}
		return out
	}
	if got := ids(download.VPNOnly); len(got) != 1 || got[0] != tunnelPath("lo") {
		t.Fatalf("VPN-only torrents get %v, want the tunnel alone", got)
	}
	if got := ids(download.VPNOff); len(got) != 1 || got[0] != "lo" {
		t.Fatalf("torrents without VPN get %v", got)
	}
	if got := ids(download.VPNAlongside); len(got) != 2 {
		t.Fatalf("torrents alongside get %v, want the interface and its tunnel", got)
	}

	// Every socket the VPN-only session can open lives on the tunnel's stack:
	// peers, uTP and DHT, trackers and the tunnel's own DNS.
	ifc := e.torrentIfacesFor(download.VPNOnly)[0]
	pc, err := ifc.ListenPacket(0)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if ip := pc.LocalAddr().(*net.UDPAddr).IP; !ip.Equal(net.IPv4(10, 9, 0, 2)) {
		t.Fatalf("the VPN-only session's UDP socket is on %v, not the tunnel", ip)
	}
	ln, err := ifc.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if ip := ln.Addr().(*net.TCPAddr).IP; !ip.Equal(net.IPv4(10, 9, 0, 2)) {
		t.Fatalf("the VPN-only session listens on %v, not the tunnel", ip)
	}
	c, err := ifc.Dial(t.Context(), "tcp4", "10.9.0.1:80") // only reachable through the tunnel
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestVPNOnlyTorrentPausesWithoutATunnelAndResumesWithOne(t *testing.T) {
	e := loopbackEngine(t)
	m := fmt.Sprintf("magnet:?xt=urn:btih:%040x&dn=vpn", 9)
	tor, err := e.session(download.VPNOnly).Add(torrentState(m, download.VPNOnly, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.downloads[tor.ID()], e.order = tor, append(e.order, tor.ID())
	e.mu.Unlock()

	e.schedule()
	if !tor.WaitingForVPN() || tor.Active() {
		t.Fatalf("with no tunnel the VPN-only torrent is %q (active %v)", tor.Status(), tor.Active())
	}
	clientPriv, clientPub, _ := tunnel.GenerateKey()
	importConf(t, e, newVPNServer(t, clientPub), clientPriv)
	waitTunnelUp(t, e)
	e.schedule()
	e.schedule() // the first re-queues it, the next starts it
	if !tor.Active() {
		t.Fatalf("with a tunnel up the torrent is still %q", tor.Status())
	}
}
