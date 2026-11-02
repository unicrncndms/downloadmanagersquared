package pia

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/tunnel"
)

func dial(ctx context.Context, network, addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

// fakePIA serves the token and server list endpoints, and one WireGuard
// server's addKey over TLS with its own CA.
type fakePIA struct {
	api    API
	server Server
	keys   []string // public keys registered
}

func newFakePIA(t *testing.T) *fakePIA {
	t.Helper()
	f := &fakePIA{}
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if r.Method != http.MethodPost || r.FormValue("username") != "p0123456" || r.FormValue("password") != "right" {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"message":"Unauthorized"}`)
				return
			}
			fmt.Fprint(w, `{"token":"TOKEN"}`)
		case "/servers":
			fmt.Fprintf(w, `{"groups":{},"regions":[{"id":"de_frankfurt","name":"DE Frankfurt","port_forward":true,"geo":false,"servers":{"meta":[{"ip":"127.0.0.1","cn":"frankfurt"}],"wg":[{"ip":"127.0.0.1","cn":"example.com"}]}},{"id":"us_east","name":"US East","port_forward":false,"geo":false,"servers":{"meta":[{"ip":"127.0.0.2","cn":"east"}],"wg":[{"ip":"127.0.0.2","cn":"east"}]}}]}`+"\n\nSIGNATURE+BLOB==\n")
		}
	}))
	t.Cleanup(web.Close)
	keySrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/addKey" || q.Get("pt") != "TOKEN" {
			json.NewEncoder(w).Encode(map[string]any{"status": "ERROR", "message": "Login failed!"})
			return
		}
		f.keys = append(f.keys, q.Get("pubkey"))
		_, serverPub, _ := tunnel.GenerateKey()
		json.NewEncoder(w).Encode(map[string]any{
			"status": "OK", "server_key": serverPub, "server_port": 1337, "server_ip": "127.0.0.1",
			"server_vip": "10.4.128.1", "peer_ip": "10.4.200.7", "peer_pubkey": q.Get("pubkey"),
			"dns_servers": []string{"10.0.0.243", "10.0.0.242"},
		})
	}))
	t.Cleanup(keySrv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(keySrv.Certificate())
	_, port, _ := net.SplitHostPort(keySrv.Listener.Addr().String())
	keyPort, _ := strconv.Atoi(port)
	f.api = API{TokenURL: web.URL + "/token", ServerListURL: web.URL + "/servers", CA: pool, KeyPort: keyPort}
	f.server = Server{IP: "127.0.0.1", CN: "example.com"} // the test certificate's name
	return f
}

func TestLoginGivesATokenAndAWrongPasswordSaysSo(t *testing.T) {
	f := newFakePIA(t)
	tok, err := f.api.Token(t.Context(), http.DefaultClient, "p0123456", "right")
	if err != nil || tok != "TOKEN" {
		t.Fatalf("token %q, %v", tok, err)
	}
	if _, err := f.api.Token(t.Context(), http.DefaultClient, "p0123456", "wrong"); !errors.Is(err, ErrLogin) {
		t.Fatalf("wrong password gave %v, want ErrLogin", err)
	}
}

func TestServerListIgnoresTheSignature(t *testing.T) {
	f := newFakePIA(t)
	regions, err := f.api.Regions(t.Context(), http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 2 || regions[0].ID != "de_frankfurt" || !regions[0].PortForward || regions[0].WG()[0].CN != "example.com" {
		t.Fatalf("regions %+v", regions)
	}
}

func TestFastestSkipsRegionsThatDontAnswer(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	api := API{MetaPort: atoi(port)}
	regions := []Region{
		{ID: "dead", Servers: Servers{Meta: []Server{{IP: "127.0.0.2"}}}}, // nothing listens there
		{ID: "alive", Servers: Servers{Meta: []Server{{IP: "127.0.0.1"}}}},
	}
	r, err := api.Fastest(t.Context(), regions, dial)
	if err != nil || r.ID != "alive" {
		t.Fatalf("fastest = %q, %v", r.ID, err)
	}
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func TestAddKeyBuildsATunnelConf(t *testing.T) {
	f := newFakePIA(t)
	priv, pub, _ := tunnel.GenerateKey()
	k, err := f.api.AddKey(t.Context(), dial, f.server, "TOKEN", pub)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.keys) != 1 || f.keys[0] != pub {
		t.Fatalf("registered %v, want our public key (with its + / = intact)", f.keys)
	}
	conf := k.Conf(priv, f.server)
	cfg, err := tunnel.ParseConf(conf)
	if err != nil {
		t.Fatalf("%v in\n%s", err, conf)
	}
	if cfg.Address.String() != "10.4.200.7" || cfg.Endpoint != "127.0.0.1:1337" || len(cfg.DNS) == 0 || cfg.DNS[0].String() != "10.0.0.243" || cfg.Keepalive != 25 {
		t.Fatalf("tunnel config %+v from\n%s", cfg, conf)
	}
	if k.ServerVIP != "10.4.128.1" {
		t.Fatalf("server_vip = %q", k.ServerVIP)
	}
}

func TestAddKeyRefusesAServerWithoutPIAsCertificate(t *testing.T) {
	f := newFakePIA(t)
	f.api.CA = x509.NewCertPool() // not the CA that signed the server
	_, pub, _ := tunnel.GenerateKey()
	if _, err := f.api.AddKey(t.Context(), dial, f.server, "TOKEN", pub); err == nil {
		t.Fatal("registered a key with a server PIA's CA didn't sign")
	}
	f = newFakePIA(t)
	if _, err := f.api.AddKey(t.Context(), dial, f.server, "EXPIRED", pub); err == nil || !strings.Contains(err.Error(), "Login failed") {
		t.Fatalf("a refused key gave %v", err)
	}
}

func TestDefaultAPIHasPIAsCA(t *testing.T) {
	if Default.CA == nil || Default.KeyPort != 1337 || !strings.HasPrefix(Default.TokenURL, "https://www.privateinternetaccess.com/") {
		t.Fatalf("default API %+v", Default)
	}
}

func TestPortForwardingSignsThenBinds(t *testing.T) {
	var binds []string
	expires := time.Now().Add(60 * 24 * time.Hour).UTC().Truncate(time.Second)
	payload := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf(`{"token":"x","port":47123,"expires_at":%q}`, expires.Format(time.RFC3339Nano))))
	pf := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/getSignature":
			if q.Get("token") != "TOKEN" {
				json.NewEncoder(w).Encode(map[string]string{"status": "ERROR", "message": "bad token"})
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"status": "OK", "payload": payload, "signature": "SIG/+="})
		case "/bindPort":
			binds = append(binds, q.Get("payload")+"|"+q.Get("signature"))
			json.NewEncoder(w).Encode(map[string]string{"status": "OK", "message": "port scheduled for add"})
		}
	}))
	defer pf.Close()
	ca := x509.NewCertPool()
	ca.AddCert(pf.Certificate())
	_, port, _ := net.SplitHostPort(pf.Listener.Addr().String())
	api := API{CA: ca, PFPort: atoi(port)}
	gw := Server{IP: "127.0.0.1", CN: "example.com"}

	sig, err := api.Signature(t.Context(), dial, gw, "TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if sig.Port != 47123 || !sig.Expires.Equal(expires) {
		t.Fatalf("signature for port %d until %v, want 47123 until %v", sig.Port, sig.Expires, expires)
	}
	if err := api.BindPort(t.Context(), dial, gw, sig); err != nil {
		t.Fatal(err)
	}
	if len(binds) != 1 || binds[0] != payload+"|SIG/+=" {
		t.Fatalf("bound with %v, want the payload and signature as received", binds)
	}
	if _, err := api.Signature(t.Context(), dial, gw, "OLD"); err == nil {
		t.Fatal("a refused signature request succeeded")
	}
}
