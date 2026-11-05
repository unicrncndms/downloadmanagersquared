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
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/netif"
	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/torrent"
	"downloadmanagersquared/engine/internal/tunnel"
)

// vpnServer is a WireGuard server on loopback serving a file at 203.0.113.1 (a
// documentation address, public as far as the proxies are concerned),
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
	cfg, err := tunnel.ParseConf(fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 203.0.113.1/24\nDNS = 203.0.113.1\n[Peer]\nPublicKey = %s\nAllowedIPs = 10.9.0.2/32\n", priv, clientPub))
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
	conf := fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.9.0.2/32\nDNS = 203.0.113.1\n[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = %s\nPersistentKeepalive = 25\n",
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

	id, err := e.Add(AddRequest{URL: "http://203.0.113.1/vpn.bin", VPN: "vpn"})
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
		if path != paths.Tunnel("lo") {
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
		for _, u := range e.Paths(paths.VPNOnly) {
			if u.ID == paths.Tunnel("lo") {
				return true
			}
		}
		return false
	})
	v := ifaceView(t, e, paths.Tunnel("lo"))
	if !v.Tunnel || !v.Present || v.Name != "VPN via Loopback" || v.SocksPort == 0 {
		t.Fatalf("tunnel shown as %+v", v)
	}
	// The browser resolves landing pages through this proxy: it must reach what the tunnel reaches.
	proxy, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", v.SocksPort))
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}, Timeout: 20 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, "http://203.0.113.1/vpn.bin", nil)
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
		return len(e.Paths(paths.VPNOnly)) > 0
	})
}

func TestVPNOnlyTorrentsGetNoBareSocket(t *testing.T) {
	e := loopbackEngine(t)
	clientPriv, clientPub, _ := tunnel.GenerateKey()
	srv := newVPNServer(t, clientPub)
	importConf(t, e, srv, clientPriv)
	waitTunnelUp(t, e)

	// Every socket the VPN-only session can open lives on the tunnel's stack:
	// peers, uTP and DHT, trackers and the tunnel's own DNS.
	ifs := e.torrentIfacesFor(paths.VPNOnly)
	if len(ifs) != 1 || ifs[0].ID != paths.Tunnel("lo") {
		t.Fatalf("VPN-only torrents get %d paths, want the tunnel alone", len(ifs))
	}
	ifc := ifs[0]
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
	c, err := ifc.Dial(t.Context(), "tcp4", "203.0.113.1:80") // only reachable through the tunnel
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestVPNOnlyTorrentPausesWithoutATunnelAndResumesWithOne(t *testing.T) {
	e := loopbackEngine(t)
	m := fmt.Sprintf("magnet:?xt=urn:btih:%040x&dn=vpn", 9)
	tor, err := e.session(paths.VPNOnly).Add(torrentState(m, paths.VPNOnly, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	e.queue.Add(tor)

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

func TestNewTorrentAsksAboutTheVPNBeforeJoiningTheSwarm(t *testing.T) {
	e := loopbackEngine(t)
	if e.settings.VPNTorrent != paths.Off {
		t.Fatalf("torrents default to %q, want off", e.settings.VPNTorrent)
	}
	id, err := e.Add(AddRequest{URL: testMagnet})
	if err != nil {
		t.Fatal(err)
	}
	j, _ := e.queue.Get(id)
	e.schedule()
	if s := summaryOf(t, e, id); s.Status != torrent.StatusChoosing || !s.AskVPN || j.(*torrent.Torrent).Active() {
		t.Fatalf("new magnet is %q, asking %v, active %v; want it waiting for the VPN question, out of the swarm", s.Status, s.AskVPN, j.(*torrent.Torrent).Active())
	}

	// Use VPN: it joins only through tunnels, and with none up it waits.
	if _, err := e.Call(nil, "chooseVpn", []byte(`{"id":"`+id+`","vpn":"vpn"}`)); err != nil {
		t.Fatal(err)
	}
	j, _ = e.queue.Get(id)
	tor := j.(*torrent.Torrent)
	if tor.State().VPN != paths.VPNOnly || tor.State().AskVPN {
		t.Fatalf("after choosing the VPN the torrent runs under %q (asking %v)", tor.State().VPN, tor.State().AskVPN)
	}
	e.schedule()
	if !tor.WaitingForVPN() {
		t.Fatalf("with no tunnel up the VPN torrent is %q", tor.Status())
	}
}

func TestStayingOnTheCurrentConnectionJoinsAtOnce(t *testing.T) {
	e := loopbackEngine(t)
	id, _ := e.Add(AddRequest{URL: testMagnet})
	if _, err := e.Call(nil, "chooseVpn", []byte(`{"id":"`+id+`","vpn":"off"}`)); err != nil {
		t.Fatal(err)
	}
	j, _ := e.queue.Get(id)
	tor := j.(*torrent.Torrent)
	if !tor.Active() || tor.Status() != torrent.StatusMetadata || tor.State().VPN != paths.Off {
		t.Fatalf("staying on the connection: %q, active %v, VPN %q", tor.Status(), tor.Active(), tor.State().VPN)
	}
}

func TestChosenVPNModeAsksNothing(t *testing.T) {
	e := loopbackEngine(t)
	id, _ := e.Add(AddRequest{URL: testMagnet, VPN: "off"}) // chosen in the add box
	if summaryOf(t, e, id).AskVPN {
		t.Fatal("asked about the VPN although the mode was chosen when adding")
	}
	e.settings.VPNTorrent = paths.VPNOnly // VPN by default: nothing to suggest
	id, _ = e.Add(AddRequest{URL: fmt.Sprintf("magnet:?xt=urn:btih:%040x", 5)})
	if summaryOf(t, e, id).AskVPN {
		t.Fatal("asked about the VPN although torrents use it by default")
	}
}

func TestTorrentFileTakesTheVPNChoiceWithItsFiles(t *testing.T) {
	e := loopbackEngine(t)
	data := make([]byte, 100<<10)
	path := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(path, data, 0o644)
	info := metainfo.Info{PieceLength: 64 << 10}
	info.BuildFromFilePath(path)
	ib, _ := bencode.Marshal(info)
	var buf bytes.Buffer
	(&metainfo.MetaInfo{InfoBytes: ib}).Write(&buf)
	id, err := e.addTorrentFile(buf.Bytes(), "")
	if err != nil {
		t.Fatal(err)
	}
	if s := summaryOf(t, e, id); !s.AskVPN || len(s.Files) != 1 {
		t.Fatalf("torrent file: asking %v, files %v", s.AskVPN, s.Files)
	}
	if _, err := e.Call(nil, "chooseFiles", []byte(`{"id":"`+id+`","files":[true],"vpn":"vpn"}`)); err != nil {
		t.Fatal(err)
	}
	j, _ := e.queue.Get(id)
	if st := j.(*torrent.Torrent).State(); st.VPN != paths.VPNOnly || st.Status != download.StatusQueued {
		t.Fatalf("after the add step: VPN %q, status %q", st.VPN, st.Status)
	}
}

func TestAConfIsRefusedOnASecondConnection(t *testing.T) {
	e := loopbackEngine(t)
	priv, _, _ := tunnel.GenerateKey()
	other, _, _ := tunnel.GenerateKey()
	_, peer, _ := tunnel.GenerateKey()
	conf := func(key string) string {
		return fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.9.0.2/32\nDNS = 203.0.113.1\n[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = 127.0.0.1:9\n", key, peer)
	}
	importOn := func(iface, conf string) error {
		p, _ := json.Marshal(map[string]string{"iface": iface, "conf": conf})
		_, err := e.Call(nil, "importTunnel", p)
		return err
	}
	if err := importOn("lo", conf(priv)); err != nil {
		t.Fatal(err)
	}
	err := importOn("wifi", conf(priv))
	if err == nil || !strings.Contains(err.Error(), "Loopback already uses this config") {
		t.Fatalf("the same config on a second connection: %v, want a refusal naming Loopback", err)
	}
	if err := importOn("lo", conf(priv)); err != nil {
		t.Fatalf("importing it again on its own connection: %v", err)
	}
	if err := importOn("wifi", conf(other)); err != nil {
		t.Fatalf("a config of its own on the second connection: %v", err)
	}
}

func setVPNSettings(t *testing.T, e *Engine, patch string) {
	t.Helper()
	if _, err := e.Call(nil, "setSettings", []byte(patch)); err != nil {
		t.Fatal(err)
	}
}

func TestVPNForDownloadsMovesEveryUnfinishedDownload(t *testing.T) {
	e := loopbackEngine(t)
	a, _ := e.Add(AddRequest{URL: "http://203.0.113.1/a.bin", VPN: "off"})
	b, _ := e.Add(AddRequest{URL: "http://203.0.113.1/b.bin", VPN: "alongside"})
	da, _ := e.get(a)
	db, _ := e.get(b)
	e.schedule()

	setVPNSettings(t, e, `{"vpnHttp":"vpn"}`)
	if da.VPN() != paths.VPNOnly || db.VPN() != paths.VPNOnly {
		t.Fatalf("after VPN only for downloads: a %q, b %q", da.VPN(), db.VPN())
	}
	waitFor(t, "the downloads to wait for a tunnel", 10*time.Second, func() bool {
		e.schedule()
		return da.WaitingForVPN() && db.WaitingForVPN()
	})

	setVPNSettings(t, e, `{"vpnHttp":"off"}`)
	if da.VPN() != paths.Off || db.VPN() != paths.Off {
		t.Fatalf("after Off for downloads: a %q, b %q", da.VPN(), db.VPN())
	}
	waitFor(t, "the downloads to leave the VPN wait", 10*time.Second, func() bool {
		e.schedule()
		return !da.WaitingForVPN() && !db.WaitingForVPN()
	})
}

func TestVPNForTorrentsMovesEveryTorrentToThatSession(t *testing.T) {
	e := loopbackEngine(t)
	id, _ := e.Add(AddRequest{URL: testMagnet})
	if _, err := e.Call(nil, "chooseVpn", []byte(`{"id":"`+id+`","vpn":"off"}`)); err != nil {
		t.Fatal(err)
	}
	asking, _ := e.Add(AddRequest{URL: fmt.Sprintf("magnet:?xt=urn:btih:%040x", 6)}) // still asked about the VPN
	j, _ := e.queue.Get(id)
	old := j.(*torrent.Torrent)
	if !old.Active() {
		t.Fatal("the torrent should be in the swarm before the setting changes")
	}

	setVPNSettings(t, e, `{"vpnTorrent":"vpn"}`)
	j, _ = e.queue.Get(id)
	tor := j.(*torrent.Torrent)
	if old.Active() || tor == old || tor.State().VPN != paths.VPNOnly {
		t.Fatalf("after VPN only for torrents: old active %v, same object %v, mode %q", old.Active(), tor == old, tor.State().VPN)
	}
	e.schedule()
	if !tor.WaitingForVPN() {
		t.Fatalf("with no tunnel up the moved torrent is %q", tor.Status())
	}
	if s := summaryOf(t, e, asking); !s.AskVPN {
		t.Fatal("a torrent still asked about the VPN lost its question")
	}
	if _, err := e.Call(nil, "setVpn", []byte(`{"id":"`+id+`","vpn":"off"}`)); err == nil {
		t.Fatal("a single download's VPN mode can still be set on its own")
	}
}

func TestAConfWithoutDNSIsRefusedSayingWhy(t *testing.T) {
	e := loopbackEngine(t)
	priv, _, _ := tunnel.GenerateKey()
	_, peer, _ := tunnel.GenerateKey()
	conf := fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.9.0.2/32\n[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = 127.0.0.1:9\n", priv, peer)
	p, _ := json.Marshal(map[string]string{"iface": "lo", "conf": conf})
	_, err := e.Call(nil, "importTunnel", p)
	if err == nil || !strings.Contains(err.Error(), "DNS") {
		t.Fatalf("got %v, want a refusal saying it needs DNS", err)
	}
	if _, err := os.Stat(e.tunnelFile("lo")); err == nil {
		t.Fatal("the refused config was saved")
	}
}

func TestASavedConfWithoutDNSKeepsItsTunnelDownSayingWhy(t *testing.T) {
	shown := balloons(t)
	dir := t.TempDir()
	e, err := open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	conf := e.tunnelFile("lo")
	e.Shutdown()
	priv, _, _ := tunnel.GenerateKey()
	_, peer, _ := tunnel.GenerateKey()
	os.MkdirAll(filepath.Dir(conf), 0o755)
	os.WriteFile(conf, []byte(fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.9.0.2/32\n[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = 127.0.0.1:9\n", priv, peer)), 0o600)

	e, err = open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	e.mu.Lock()
	e.refreshInterfacesLocked(loopbackIfaces())
	e.mu.Unlock()
	for i := 0; i < 3; i++ {
		e.syncTunnels(time.Now().Add(time.Duration(i) * time.Hour))
		time.Sleep(50 * time.Millisecond)
	}
	e.mu.Lock()
	views := e.tunnelViewsLocked(nil)
	te := e.tunnels["lo"]
	e.mu.Unlock()
	if len(views) != 1 || views[0].TunnelState != "down" || !strings.Contains(views[0].TunnelError, "DNS") {
		t.Fatalf("views %+v, want one tunnel down saying it needs DNS", views)
	}
	if te == nil || te.t != nil || te.starting {
		t.Fatal("a tunnel without DNS was started")
	}
	if _, err := os.Stat(conf); err != nil {
		t.Fatalf("the saved config was set aside: %v", err)
	}
	if len(*shown) != 0 {
		t.Fatalf("balloons %q, want none: the error shows on the tunnel", *shown)
	}
}

func TestAConfTheServerNoLongerKnowsSaysItMayHaveExpired(t *testing.T) {
	e := loopbackEngine(t)
	clientPriv, _, _ := tunnel.GenerateKey()
	_, otherPub, _ := tunnel.GenerateKey()
	importConf(t, e, newVPNServer(t, otherPub), clientPriv) // the server knows another key: revoked or expired
	waitFor(t, "the tunnel to start", 30*time.Second, func() bool {
		e.syncTunnels(time.Now())
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.tunnels["lo"].t != nil
	})
	e.syncTunnels(time.Now().Add(tunnelGrace + time.Second))
	e.mu.Lock()
	views := e.tunnelViewsLocked(nil)
	e.mu.Unlock()
	if len(views) != 1 || views[0].TunnelState != "down" || !strings.Contains(views[0].TunnelError, "expired or been revoked") {
		t.Fatalf("views %+v, want the tunnel down saying its config may have expired", views)
	}
}
