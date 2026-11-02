package engine

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/pia"
	"downloadmanagersquared/engine/internal/tunnel"
)

// fakePIA is PIA's API in front of one in-process WireGuard server, which
// accepts every key registered with addKey.
type fakePIA struct {
	api      pia.API
	logins   atomic.Int32
	keys     atomic.Int32
	password string
}

func newFakePIA(t *testing.T) *fakePIA {
	t.Helper()
	f := &fakePIA{password: "right"}
	_, dummy, _ := tunnel.GenerateKey()
	srv := newVPNServer(t, dummy)
	host, port, _ := net.SplitHostPort(srv.endpoint)
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
			fmt.Fprintf(w, `{"regions":[{"id":"nl","name":"Netherlands","port_forward":true,"servers":{"meta":[{"ip":"%s","cn":"nl"}],"wg":[{"ip":"%s","cn":"example.com"}]}}]}`+"\nSIG\n", host, host)
		}
	}))
	t.Cleanup(web.Close)
	keys := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pub := r.URL.Query().Get("pubkey")
		if r.URL.Query().Get("pt") != "TOKEN" || srv.t.AddPeer(pub, netip.MustParsePrefix("10.9.0.2/32")) != nil {
			json.NewEncoder(w).Encode(map[string]string{"status": "ERROR"})
			return
		}
		f.keys.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"status": "OK", "server_key": srv.pub, "server_port": atoi(port),
			"server_vip": "10.9.0.1", "peer_ip": "10.9.0.2", "dns_servers": []string{"10.9.0.1"}})
	}))
	t.Cleanup(keys.Close)
	ca := x509.NewCertPool()
	ca.AddCert(keys.Certificate())
	_, kport, _ := net.SplitHostPort(keys.Listener.Addr().String())
	f.api = pia.API{TokenURL: web.URL + "/token", ServerListURL: web.URL + "/servers", CA: ca, KeyPort: atoi(kport), MetaPort: 1}
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
	if v := ifaceView(t, e, tunnelPath("lo")); !v.Present || v.Name != "VPN via Loopback" {
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
	for _, u := range e.Usable(download.VPNOnly) {
		if u.ID == tunnelPath("lo") {
			return
		}
	}
	t.Fatal("the PIA tunnel isn't a path for VPN-only downloads")
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
