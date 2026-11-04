package engine

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/netif"
	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/pia"
	"downloadmanagersquared/engine/internal/tunnel"
)

// fakePIA is PIA's API in front of one in-process WireGuard server, which
// accepts every key registered with addKey.
type fakePIA struct {
	api        pia.API
	logins     atomic.Int32
	keys       atomic.Int32
	signatures atomic.Int32
	binds      atomic.Int32
	password   string
	vpn        *vpnServer
}

func newFakePIA(t *testing.T) *fakePIA { return newFakePIAWith(t, false, 1) }

// newFakePIAForwarding is newFakePIA in a region that may forward ports: then
// the server answers port forwarding inside the tunnel at 203.0.113.1:19999.
func newFakePIAForwarding(t *testing.T, forwards bool) *fakePIA {
	return newFakePIAWith(t, forwards, 1)
}

// newFakePIAWith is a region of n WireGuard servers. Like PIA's, a server gives
// every key of the account the same peer IP, so its latest key takes the IP over.
func newFakePIAWith(t *testing.T, forwards bool, n int) *fakePIA {
	t.Helper()
	f := &fakePIA{password: "right"}
	servers := map[string]*vpnServer{}
	var wg []string
	var host string
	for i := range n {
		_, dummy, _ := tunnel.GenerateKey()
		s := newVPNServer(t, dummy)
		cn := "example.com"
		if n > 1 {
			cn = fmt.Sprintf("s%d.example.com", i)
		}
		if i == 0 {
			f.vpn = s
		}
		servers[cn] = s
		host, _, _ = net.SplitHostPort(s.endpoint)
		wg = append(wg, fmt.Sprintf(`{"ip":"%s","cn":"%s"}`, host, cn))
	}
	srv := f.vpn
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			f.logins.Add(1)
			if r.FormValue("username") != "p0123456" || r.FormValue("password") != f.password {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"token":"TOKEN"}`)
		case "/servers":
			fmt.Fprintf(w, `{"regions":[{"id":"nl","name":"Netherlands","port_forward":%v,"servers":{"meta":[{"ip":"%s","cn":"nl"}],"wg":[%s]}}]}`+"\nSIG\n", forwards, host, strings.Join(wg, ","))
		}
	}))
	t.Cleanup(web.Close)
	keys := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pub := r.URL.Query().Get("pubkey")
		cn, _, _ := net.SplitHostPort(r.Host)
		s := servers[cn]
		if r.URL.Query().Get("pt") != "TOKEN" || s == nil || s.t.AddPeer(pub, netip.MustParsePrefix("10.9.0.2/32")) != nil {
			json.NewEncoder(w).Encode(map[string]string{"status": "ERROR"})
			return
		}
		f.keys.Add(1)
		_, port, _ := net.SplitHostPort(s.endpoint)
		json.NewEncoder(w).Encode(map[string]any{"status": "OK", "server_key": s.pub, "server_port": atoi(port),
			"server_vip": "203.0.113.1", "peer_ip": "10.9.0.2", "dns_servers": []string{"203.0.113.1"}})
	}))
	t.Cleanup(keys.Close)
	ca := x509.NewCertPool()
	ca.AddCert(keys.Certificate())
	_, kport, _ := net.SplitHostPort(keys.Listener.Addr().String())
	f.api = pia.API{TokenURL: web.URL + "/token", ServerListURL: web.URL + "/servers", CA: ca, KeyPort: atoi(kport), MetaPort: 1, PFPort: 19999}

	pfLn, err := srv.t.Listen(19999) // inside the tunnel, as on PIA's servers
	if err != nil {
		t.Fatal(err)
	}
	payload := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf(`{"port":47000,"expires_at":%q}`, time.Now().Add(1440*time.Hour).Format(time.RFC3339))))
	pf := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/getSignature":
			f.signatures.Add(1)
			json.NewEncoder(w).Encode(map[string]string{"status": "OK", "payload": payload, "signature": "SIG"})
		case "/bindPort":
			f.binds.Add(1)
			json.NewEncoder(w).Encode(map[string]string{"status": "OK"})
		}
	}))
	pf.Listener = pfLn
	pf.StartTLS()
	t.Cleanup(pf.Close)
	f.vpn = srv
	return f
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func setPIA(t *testing.T, e *Engine, user, pass string) {
	t.Helper()
	p, _ := json.Marshal(map[string]string{"user": user, "pass": pass, "region": "nl"})
	if _, err := e.Call(nil, "setPia", p); err != nil {
		t.Fatal(err)
	}
}

func TestPIALoginGivesEveryInterfaceATunnel(t *testing.T) {
	f := newFakePIA(t)
	e := loopbackEngine(t)
	e.piaAPI = f.api
	setPIA(t, e, "p0123456", "right")
	waitTunnelUp(t, e)
	if v := ifaceView(t, e, paths.Tunnel("lo")); !v.Present || v.Name != "VPN via Loopback" {
		t.Fatalf("PIA tunnel shown as %+v", v)
	}
	e.updateSnapshot(0)
	if s := e.Snapshot().PIA; s.State != "ok" || s.Region != "Netherlands" {
		t.Fatalf("PIA shown as %+v", s)
	}
	// The login stays in the engine, protected; the extension never gets it back.
	b, _ := os.ReadFile(filepath.Join(e.dir, "pia.dat"))
	st, _ := json.Marshal(e.Snapshot())
	set, _ := json.Marshal(e.settings)
	for _, leak := range [][]byte{st, set} {
		if bytes.Contains(leak, []byte(`right`)) || bytes.Contains(leak, []byte("p0123456")) {
			t.Fatalf("the PIA login reaches the extension: %s", leak)
		}
	}
	if len(b) == 0 || bytes.Contains(b, []byte("right")) {
		t.Fatal("pia.dat is missing or holds the password in the clear")
	}
	for _, u := range e.Paths(paths.VPNOnly) {
		if u.ID == paths.Tunnel("lo") {
			return
		}
	}
	t.Fatal("the PIA tunnel isn't a path for VPN-only downloads")
}

// PIA gives an account one peer IP per server, so two tunnels on one server
// steal it from each other and only the newest carries traffic.
func TestPIATunnelsOfTwoInterfacesEachCarryTraffic(t *testing.T) {
	f := newFakePIAWith(t, false, 2)
	e := loopbackEngine(t)
	lo := loopbackIfaces()[0]
	lo2 := lo
	lo2.ID, lo2.Name = "lo2", "Loopback 2"
	e.mu.Lock()
	e.refreshInterfacesLocked([]netif.Interface{lo, lo2})
	e.mu.Unlock()
	e.piaAPI = f.api
	setPIA(t, e, "p0123456", "right")
	waitFor(t, "both tunnels to come up", 30*time.Second, func() bool {
		e.syncTunnels(time.Now())
		return len(e.Paths(paths.VPNOnly)) == 2
	})
	for _, id := range []string{"lo", "lo2"} {
		e.mu.Lock()
		c := e.tunnels[id].client
		e.mu.Unlock()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodHead, "http://203.0.113.1/", nil)
		resp, err := c.Do(req)
		cancel()
		if err != nil {
			t.Fatalf("the tunnel of %s carries no traffic: %v", id, err)
		}
		resp.Body.Close()
	}
}

func TestPIATunnelsComeBackAfterARestart(t *testing.T) {
	f := newFakePIA(t)
	e := loopbackEngine(t)
	e.piaAPI = f.api
	setPIA(t, e, "p0123456", "right")
	waitTunnelUp(t, e)
	dir := e.dir
	e.Shutdown()

	e2, err := open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Shutdown()
	e2.piaAPI = f.api
	e2.mu.Lock()
	e2.refreshInterfacesLocked(loopbackIfaces())
	e2.mu.Unlock()
	waitTunnelUp(t, e2)
	if f.keys.Load() < 2 {
		t.Fatalf("after the restart %d keys were registered in all, want a fresh one", f.keys.Load())
	}
}

func TestRefusedPIALoginWaitsForTheUser(t *testing.T) {
	f := newFakePIA(t)
	e := loopbackEngine(t)
	e.piaAPI = f.api
	setPIA(t, e, "p0123456", "wrong")
	waitFor(t, "the login to be refused", 10*time.Second, func() bool {
		e.syncTunnels(time.Now())
		e.updateSnapshot(0)
		return e.Snapshot().PIA.State == "login-failed"
	})
	n := f.logins.Load()
	for range 3 {
		e.syncTunnels(time.Now().Add(time.Hour))
	}
	time.Sleep(200 * time.Millisecond)
	if f.logins.Load() != n {
		t.Fatalf("kept logging in with a refused password: %d tries", f.logins.Load())
	}
	f.password = "wrong" // the user fixes it on PIA's side and saves again
	setPIA(t, e, "p0123456", "wrong")
	waitTunnelUp(t, e)
}

func TestPIATunnelForwardsAPortAndTakesPeersOnIt(t *testing.T) {
	f := newFakePIAForwarding(t, true)
	e := loopbackEngine(t)
	e.piaAPI = f.api
	setPIA(t, e, "p0123456", "right")
	waitTunnelUp(t, e)
	e.forwardPorts(time.Now())
	if v := ifaceView(t, e, paths.Tunnel("lo")); v.Incoming != "reachable" {
		t.Fatalf("tunnel with a forwarded port shown as %q", v.Incoming)
	}
	// The VPN-only session listens on the forwarded port inside the tunnel,
	// where PIA sends incoming peers, while one of its torrents is in the swarm.
	e.syncTorrentIfaces()
	vpnTorrentInSwarm(t, e)
	if ifs := e.torrentIfacesFor(paths.VPNOnly); len(ifs) != 1 || ifs[0].Port != 47000 {
		t.Fatalf("VPN-only torrents listen on %+v, want port 47000", ifs)
	}
	c, err := f.vpn.t.DialContext(t.Context(), "tcp4", "10.9.0.2:47000")
	if err != nil {
		t.Fatalf("a peer coming in through PIA couldn't connect: %v", err)
	}
	c.Close()

	// PIA drops a port that isn't bound again within 15 minutes.
	e.forwardPorts(time.Now().Add(5 * time.Minute))
	if f.binds.Load() != 1 {
		t.Fatalf("bound again after 5 minutes (%d binds)", f.binds.Load())
	}
	for i := 1; i <= 5; i++ { // over an hour
		e.forwardPorts(time.Now().Add(time.Duration(i) * 15 * time.Minute))
	}
	if f.binds.Load() != 6 || f.signatures.Load() != 1 {
		t.Fatalf("an hour later: %d binds, %d signatures; want a bind every 15 minutes on one signature", f.binds.Load(), f.signatures.Load())
	}
	if v := ifaceView(t, e, paths.Tunnel("lo")); v.Incoming != "reachable" {
		t.Fatalf("after an hour the tunnel is %q", v.Incoming)
	}
}

func vpnTorrentInSwarm(t *testing.T, e *Engine) {
	t.Helper()
	tor, err := e.session(paths.VPNOnly).Add(torrentState(fmt.Sprintf("magnet:?xt=urn:btih:%040x", 77), paths.VPNOnly, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	tor.Start()
	t.Cleanup(func() { tor.Stop(download.StatusPaused) })
	waitFor(t, "the VPN-only session to go live", 5*time.Second, e.session(paths.VPNOnly).Live)
}

func TestPIARegionWithoutForwardingIsOutgoingOnly(t *testing.T) {
	f := newFakePIAForwarding(t, false)
	e := loopbackEngine(t)
	e.piaAPI = f.api
	setPIA(t, e, "p0123456", "right")
	waitTunnelUp(t, e)
	e.forwardPorts(time.Now())
	if f.signatures.Load() != 0 {
		t.Fatal("asked a region without port forwarding for a port")
	}
	if v := ifaceView(t, e, paths.Tunnel("lo")); v.Incoming != "outgoing" {
		t.Fatalf("tunnel in a region without forwarding shown as %q", v.Incoming)
	}
	if ifs := e.torrentIfacesFor(paths.VPNOnly); len(ifs) != 1 {
		t.Fatal("torrents can't use a tunnel without a forwarded port")
	}
}
