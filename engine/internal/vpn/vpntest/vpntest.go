// Package vpntest has VPN servers for tests: a WireGuard server on loopback,
// PIA's API in front of such servers, and a VPN server's NAT-PMP.
package vpntest

import (
	"bytes"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/netif"
	"downloadmanagersquared/engine/internal/pia"
	"downloadmanagersquared/engine/internal/tunnel"
)

// Server is a WireGuard server on loopback serving a file at 203.0.113.1 (a
// documentation address, public as far as the proxies are concerned), an
// address only reachable through the tunnel. Its client is 10.9.0.2.
type Server struct {
	T        *tunnel.Tunnel
	Endpoint string
	Pub      string
	Data     []byte
}

// NewServer is a server that lets in the client with key clientPub.
func NewServer(t testing.TB, clientPub string) *Server {
	t.Helper()
	return newServer(t, clientPub, "udp4", "127.0.0.1:0")
}

// NewServer6 is NewServer with its endpoint on ::1: WireGuard over IPv6,
// IPv4 inside.
func NewServer6(t testing.TB, clientPub string) *Server {
	t.Helper()
	return newServer(t, clientPub, "udp6", "[::1]:0")
}

func newServer(t testing.TB, clientPub, network, addr string) *Server {
	t.Helper()
	priv, pub, err := tunnel.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := tunnel.ParseConf(fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 203.0.113.1/24\nDNS = 203.0.113.1\n[Peer]\nPublicKey = %s\nAllowedIPs = 10.9.0.2/32\n", priv, clientPub))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := tunnel.Start(cfg, func(bool) (net.PacketConn, error) { return net.ListenPacket(network, addr) }, nil)
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
	return &Server{T: srv, Endpoint: srv.LocalAddr().String(), Pub: pub, Data: data}
}

// Conf is the config a VPN provider would hand the client with key clientPriv.
func (s *Server) Conf(clientPriv string) string {
	return fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.9.0.2/32\nDNS = 203.0.113.1\n[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = %s\nPersistentKeepalive = 25\n",
		clientPriv, s.Pub, s.Endpoint)
}

// Loopback is this machine's loopback as an interface with id and name.
func Loopback(id, name string) netif.Interface {
	ifs, _ := net.Interfaces()
	var idx uint32
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 {
			idx = uint32(i.Index)
		}
	}
	return netif.Interface{ID: id, Name: name, Index: idx, IPv4: net.IPv4(127, 0, 0, 1)}
}

// PIA is PIA's API in front of in-process WireGuard servers, which accept
// every key registered with addKey. The account is p0123456 with Password.
type PIA struct {
	API        pia.API
	Logins     atomic.Int32
	Keys       atomic.Int32
	Signatures atomic.Int32
	Binds      atomic.Int32
	Password   string
	VPN        *Server // the first server
	// Unanswering hands out a server key no server has, so tunnels start but
	// never get an answer. Set it before the first tunnel starts.
	Unanswering bool
}

// NewPIA is a region "nl" (Netherlands) of n WireGuard servers. If it forwards
// ports, the first server answers port forwarding inside the tunnel at
// 203.0.113.1:19999 with port 47000. Like PIA's, a server gives every key of
// the account the same peer IP, so its latest key takes the IP over.
func NewPIA(t testing.TB, forwards bool, n int) *PIA {
	t.Helper()
	f := &PIA{Password: "right"}
	servers := map[string]*Server{}
	var wg []string
	var host string
	for i := range n {
		_, dummy, _ := tunnel.GenerateKey()
		s := NewServer(t, dummy)
		cn := "example.com"
		if n > 1 {
			cn = fmt.Sprintf("s%d.example.com", i)
		}
		if i == 0 {
			f.VPN = s
		}
		servers[cn] = s
		host, _, _ = net.SplitHostPort(s.Endpoint)
		wg = append(wg, fmt.Sprintf(`{"ip":"%s","cn":"%s"}`, host, cn))
	}
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			f.Logins.Add(1)
			if r.FormValue("username") != "p0123456" || r.FormValue("password") != f.Password {
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
		if r.URL.Query().Get("pt") != "TOKEN" || s == nil || s.T.AddPeer(pub, netip.MustParsePrefix("10.9.0.2/32")) != nil {
			json.NewEncoder(w).Encode(map[string]string{"status": "ERROR"})
			return
		}
		f.Keys.Add(1)
		_, port, _ := net.SplitHostPort(s.Endpoint)
		key := s.Pub
		if f.Unanswering {
			_, key, _ = tunnel.GenerateKey()
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "OK", "server_key": key, "server_port": atoi(port),
			"server_vip": "203.0.113.1", "peer_ip": "10.9.0.2", "dns_servers": []string{"203.0.113.1"}})
	}))
	t.Cleanup(keys.Close)
	ca := x509.NewCertPool()
	ca.AddCert(keys.Certificate())
	_, kport, _ := net.SplitHostPort(keys.Listener.Addr().String())
	f.API = pia.API{TokenURL: web.URL + "/token", ServerListURL: web.URL + "/servers", CA: ca, KeyPort: atoi(kport), MetaPort: 1, PFPort: 19999}

	pfLn, err := f.VPN.T.Listen(19999) // inside the tunnel, as on PIA's servers
	if err != nil {
		t.Fatal(err)
	}
	payload := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf(`{"port":47000,"expires_at":%q}`, time.Now().Add(1440*time.Hour).Format(time.RFC3339))))
	pf := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/getSignature":
			f.Signatures.Add(1)
			json.NewEncoder(w).Encode(map[string]string{"status": "OK", "payload": payload, "signature": "SIG"})
		case "/bindPort":
			f.Binds.Add(1)
			json.NewEncoder(w).Encode(map[string]string{"status": "OK"})
		}
	}))
	pf.Listener = pfLn
	pf.StartTLS()
	t.Cleanup(pf.Close)
	return f
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// PMP is a VPN server's NAT-PMP, answering inside its tunnel the way Proton's
// is documented to: the public port it picks, for at most 60 s. Its server
// sits at a documentation address, which real configs' gateways never are:
// tests must allow it as a gateway.
type PMP struct {
	mu        sync.Mutex
	Public    int
	Lifetime  uint32
	RefuseUDP bool
	Silent    bool
	Drop      int // mapping requests left to ignore, like lost packets
	tcp, udp  int // mappings granted
	probes    int
}

func ServePMP(t testing.TB, srv *Server) *PMP {
	t.Helper()
	f := &PMP{Public: 51234, Lifetime: 60}
	pc, err := srv.T.ListenPacket(5351)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 64)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			f.mu.Lock()
			switch {
			case f.Silent || n < 2 || buf[0] != 0:
			case buf[1] == 0:
				f.probes++
				pc.WriteTo([]byte{0, 128, 0, 0, 0, 0, 0, 1, 198, 51, 100, 9}, from)
			case n == 12 && (buf[1] == 1 || buf[1] == 2) && f.Drop > 0:
				f.Drop--
			case n == 12 && (buf[1] == 1 || buf[1] == 2):
				resp := make([]byte, 16)
				resp[1] = 128 + buf[1]
				life := min(binary.BigEndian.Uint32(buf[8:]), f.Lifetime)
				if buf[1] == 1 && f.RefuseUDP {
					binary.BigEndian.PutUint16(resp[2:], 2)
				} else if buf[1] == 1 {
					f.udp++
				} else {
					f.tcp++
				}
				binary.BigEndian.PutUint16(resp[10:], uint16(f.Public))
				binary.BigEndian.PutUint32(resp[12:], life)
				pc.WriteTo(resp, from)
			}
			f.mu.Unlock()
		}
	}()
	return f
}

// Set changes how it answers.
func (f *PMP) Set(fn func(f *PMP)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}

// Counts are the mappings granted and the probes answered so far.
func (f *PMP) Counts() (tcp, udp, probes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tcp, f.udp, f.probes
}

// AnyGateway lets NAT-PMP go to the test server's documentation address.
func AnyGateway(netip.Addr) bool { return true }
