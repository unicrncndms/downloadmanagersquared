package vpn

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/netif"
	"downloadmanagersquared/engine/internal/rate"
	"downloadmanagersquared/engine/internal/tunnel"
	"downloadmanagersquared/engine/internal/vpn/vpntest"
)

// rig is Tunnels over loopback interfaces, ticked by hand.
type rig struct {
	*Tunnels
	dir    string
	o      Options
	ifaces []Iface
}

func newRig(t *testing.T, o Options, ids ...string) *rig {
	t.Helper()
	if len(ids) == 0 {
		ids = []string{"lo"}
	}
	r := &rig{dir: t.TempDir(), o: o}
	r.o.Dir = r.dir
	for i, id := range ids {
		name := "Loopback"
		if i > 0 {
			name = fmt.Sprintf("Loopback %d", i+1)
		}
		b := netif.NewBinding(vpntest.Loopback(id, name), rate.NewShaper())
		r.ifaces = append(r.ifaces, Iface{ID: id, Name: name, Binding: b, Usable: true})
	}
	r.open(t)
	return r
}

// open starts Tunnels on the rig's folder, as at an engine start.
func (r *rig) open(t *testing.T) []Problem {
	t.Helper()
	r.Tunnels = New(r.o)
	tt := r.Tunnels
	t.Cleanup(tt.Close)
	return tt.Load()
}

// tick is one Tick at now, waiting for the work it started.
func (r *rig) tick(now time.Time) {
	r.Tick(now, r.ifaces)
	r.Wait()
}

func (r *rig) iface(id string) *Iface {
	for i := range r.ifaces {
		if r.ifaces[i].ID == id {
			return &r.ifaces[i]
		}
	}
	return nil
}

func (r *rig) waitUp(t *testing.T, ids ...string) {
	t.Helper()
	if len(ids) == 0 {
		ids = []string{"lo"}
	}
	waitFor(t, "the tunnels to come up", 30*time.Second, func() bool {
		r.tick(time.Now())
		for _, id := range ids {
			if _, ok := r.Up(id); !ok {
				return false
			}
		}
		return true
	})
}

func (r *rig) status(t *testing.T, id string, at time.Time) Status {
	t.Helper()
	for _, s := range r.Status(at) {
		if s.Iface == id {
			return s
		}
	}
	t.Fatalf("no tunnel on %s", id)
	return Status{}
}

func waitFor(t *testing.T, what string, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// imported is a rig with a tunnel on lo to a fresh server.
func importedTunnel(t *testing.T, o Options) (*rig, *vpntest.Server) {
	t.Helper()
	r := newRig(t, o)
	priv, pub, _ := tunnel.GenerateKey()
	srv := vpntest.NewServer(t, pub)
	if err := r.Import("lo", srv.Conf(priv)); err != nil {
		t.Fatal(err)
	}
	return r, srv
}

func TestATunnelCarriesTrafficToWhatOnlyItReaches(t *testing.T) {
	r, srv := importedTunnel(t, Options{})
	r.waitUp(t)
	up, _ := r.Up("lo")
	req, _ := http.NewRequest(http.MethodGet, "http://203.0.113.1/vpn.bin", nil)
	req.Header.Set("Range", "bytes=0-9")
	resp, err := up.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(b, srv.Data[:10]) {
		t.Fatalf("through the tunnel: %q", b)
	}
	if s := r.status(t, "lo", time.Now()); s.State != "up" || s.Provider != "conf" || s.Address != "10.9.0.2" {
		t.Fatalf("status %+v", s)
	}
}

func TestTunnelGetsItsOwnProxyForResolving(t *testing.T) {
	r, srv := importedTunnel(t, Options{})
	r.waitUp(t)
	port := r.status(t, "lo", time.Now()).SocksPort
	// The browser resolves landing pages through this proxy: it must reach what the tunnel reaches.
	proxy, _ := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", port))
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}, Timeout: 20 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, "http://203.0.113.1/vpn.bin", nil)
	req.Header.Set("Range", "bytes=0-9")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(b, srv.Data[:10]) {
		t.Fatalf("through the tunnel's proxy: %q", b)
	}
}

func TestATunnelStopsWithItsInterfaceAndStartsAgainWithIt(t *testing.T) {
	r, _ := importedTunnel(t, Options{})
	r.waitUp(t)
	first, _ := r.Up("lo")
	r.iface("lo").Usable = false
	r.tick(time.Now())
	if _, ok := r.Up("lo"); ok {
		t.Fatal("the tunnel stayed up with its interface switched off")
	}
	if s := r.status(t, "lo", time.Now()); s.State != "off" {
		t.Fatalf("with its interface off the tunnel is %q", s.State)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if _, err := first.Dial(ctx, "tcp4", "203.0.113.1:80"); err == nil {
		t.Fatal("a stopped tunnel still dials")
	}
	r.iface("lo").Usable = true
	r.waitUp(t)
	if again, _ := r.Up("lo"); again.Gen == first.Gen {
		t.Fatal("a new start has the old one's generation")
	}
}

func TestRemovingATunnelDeletesItsConfig(t *testing.T) {
	r, _ := importedTunnel(t, Options{})
	r.waitUp(t)
	if err := r.Remove("lo"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Up("lo"); ok || len(r.Status(time.Now())) != 0 {
		t.Fatal("the tunnel is still there")
	}
	if _, err := os.Stat(r.tunnelFile("lo")); err == nil {
		t.Fatal("its config is still saved")
	}
	if p := r.open(t); len(p) != 0 || len(r.Status(time.Now())) != 0 {
		t.Fatalf("after a restart: problems %v, tunnels %v", p, r.Status(time.Now()))
	}
}

func TestAConfIsRefusedOnASecondConnection(t *testing.T) {
	r := newRig(t, Options{})
	r.ifaces = append(r.ifaces, Iface{ID: "wifi", Name: "Wi-Fi"})
	r.tick(time.Now())
	priv, _, _ := tunnel.GenerateKey()
	other, _, _ := tunnel.GenerateKey()
	_, peer, _ := tunnel.GenerateKey()
	conf := func(key string) string {
		return fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.9.0.2/32\nDNS = 203.0.113.1\n[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = 127.0.0.1:9\n", key, peer)
	}
	if err := r.Import("lo", conf(priv)); err != nil {
		t.Fatal(err)
	}
	err := r.Import("wifi", conf(priv))
	if err == nil || !strings.Contains(err.Error(), "Loopback already uses this config") {
		t.Fatalf("the same config on a second connection: %v, want a refusal naming Loopback", err)
	}
	if err := r.Import("lo", conf(priv)); err != nil {
		t.Fatalf("importing it again on its own connection: %v", err)
	}
	if err := r.Import("wifi", conf(other)); err != nil {
		t.Fatalf("a config of its own on the second connection: %v", err)
	}
}

func TestAConfWithoutDNSIsRefusedSayingWhy(t *testing.T) {
	r := newRig(t, Options{})
	priv, _, _ := tunnel.GenerateKey()
	_, peer, _ := tunnel.GenerateKey()
	conf := fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.9.0.2/32\n[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = 127.0.0.1:9\n", priv, peer)
	err := r.Import("lo", conf)
	if err == nil || !strings.Contains(err.Error(), "DNS") {
		t.Fatalf("got %v, want a refusal saying it needs DNS", err)
	}
	if _, err := os.Stat(r.tunnelFile("lo")); err == nil {
		t.Fatal("the refused config was saved")
	}
}

func TestASavedConfWithoutDNSKeepsItsTunnelDownSayingWhy(t *testing.T) {
	r := newRig(t, Options{})
	conf := filepath.Join(r.dir, "tunnels", hex.EncodeToString([]byte("lo"))+".conf")
	priv, _, _ := tunnel.GenerateKey()
	_, peer, _ := tunnel.GenerateKey()
	os.MkdirAll(filepath.Dir(conf), 0o755)
	os.WriteFile(conf, []byte(fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.9.0.2/32\n[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = 127.0.0.1:9\n", priv, peer)), 0o600)

	if p := r.open(t); len(p) != 0 {
		t.Fatalf("problems %v, want none: the error shows on the tunnel", p)
	}
	for i := range 3 {
		r.tick(time.Now().Add(time.Duration(i) * time.Hour))
	}
	st := r.Status(time.Now())
	if len(st) != 1 || st[0].State != "down" || !strings.Contains(st[0].Err, "DNS") {
		t.Fatalf("status %+v, want one tunnel down saying it needs DNS", st)
	}
	if _, ok := r.Up("lo"); ok {
		t.Fatal("a tunnel without DNS was started")
	}
	if _, err := os.Stat(conf); err != nil {
		t.Fatalf("the saved config was set aside: %v", err)
	}
}

func TestAnUnreadableSavedConfIsSetAside(t *testing.T) {
	r := newRig(t, Options{})
	conf := filepath.Join(r.dir, "tunnels", hex.EncodeToString([]byte("wifi"))+".conf")
	os.MkdirAll(filepath.Dir(conf), 0o755)
	os.WriteFile(conf, []byte("[Interface]\nPrivateKey = nope\n"), 0o600)
	p := r.open(t)
	if len(p) != 1 || p[0].Iface != "wifi" {
		t.Fatalf("problems %v, want the one config", p)
	}
	if _, err := os.Stat(conf + ".broken"); err != nil {
		t.Fatal(err)
	}
	if len(r.Status(time.Now())) != 0 {
		t.Fatal("an unreadable config was loaded")
	}
}

func TestAConfTheServerNoLongerKnowsSaysItMayHaveExpired(t *testing.T) {
	r := newRig(t, Options{})
	clientPriv, _, _ := tunnel.GenerateKey()
	_, otherPub, _ := tunnel.GenerateKey()
	srv := vpntest.NewServer(t, otherPub) // the server knows another key: revoked or expired
	if err := r.Import("lo", srv.Conf(clientPriv)); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r.tick(now) // started, never answered
	r.tick(now.Add(tunnelGrace + time.Second))
	st := r.Status(time.Now())
	if len(st) != 1 || st[0].State != "down" || !strings.Contains(st[0].Err, "expired or been revoked") {
		t.Fatalf("status %+v, want the tunnel down saying its config may have expired", st)
	}
}
