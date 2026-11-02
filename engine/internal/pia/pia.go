// Package pia talks to Private Internet Access's manual-connection API: log in
// for a token, list regions, find the fastest one, and register a WireGuard key
// with a server to get a tunnel config (as in pia-foss/manual-connections).
package pia

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PIA's own CA, which signs its WireGuard and port forwarding servers
// (ca.rsa.4096.crt from pia-foss/manual-connections, valid until 2034).
//
//go:embed ca.rsa.4096.crt
var caPEM []byte

// ErrLogin means PIA refused the username or password: only the user can fix it.
var ErrLogin = errors.New("PIA refused the username or password")

type Dial func(ctx context.Context, network, addr string) (net.Conn, error)

// API is where PIA's endpoints are; tests point it at fakes.
type API struct {
	TokenURL      string
	ServerListURL string
	CA            *x509.CertPool
	KeyPort       int // addKey on WireGuard servers
	MetaPort      int // what Fastest times a connection to
	PFPort        int // port forwarding, inside the tunnel
}

var Default = API{
	TokenURL:      "https://www.privateinternetaccess.com/api/client/v2/token",
	ServerListURL: "https://serverlist.piaservers.net/vpninfo/servers/v6",
	CA:            caPool(),
	KeyPort:       1337,
	MetaPort:      443,
	PFPort:        19999,
}

func caPool() *x509.CertPool {
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(caPEM) {
		panic("pia: embedded CA doesn't parse")
	}
	return p
}

// Token logs in. A token lasts 24 hours and serves every server.
func (a API) Token(ctx context.Context, c *http.Client, user, pass string) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("username", user)
	mw.WriteField("password", pass)
	mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.TokenURL, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		Token string `json:"token"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&r)
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", ErrLogin
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("PIA login answered %s", resp.Status)
	case r.Token == "":
		return "", ErrLogin
	}
	return r.Token, nil
}

type Server struct {
	IP string `json:"ip"`
	CN string `json:"cn"` // the name its certificate is for
}

type Servers struct {
	Meta []Server `json:"meta"`
	WG   []Server `json:"wg"`
}

type Region struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	PortForward bool    `json:"port_forward"`
	Geo         bool    `json:"geo"`
	Servers     Servers `json:"servers"`
}

// WG are the region's WireGuard servers.
func (r Region) WG() []Server { return r.Servers.WG }

// Regions reads the server list. Only its first line is JSON; a signature
// follows, which PIA's own scripts don't check either.
func (a API) Regions(ctx context.Context, c *http.Client) ([]Region, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.ServerListURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("PIA server list answered %s", resp.Status)
	}
	line, err := bufio.NewReaderSize(io.LimitReader(resp.Body, 8<<20), 64<<10).ReadBytes('\n')
	if err != nil && err != io.EOF {
		return nil, err
	}
	var list struct {
		Regions []Region `json:"regions"`
	}
	if err := json.Unmarshal(line, &list); err != nil {
		return nil, fmt.Errorf("reading PIA's server list: %w", err)
	}
	var out []Region
	for _, r := range list.Regions {
		if len(r.Servers.WG) > 0 {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("PIA's server list has no WireGuard region")
	}
	return out, nil
}

// Fastest is the region whose meta server accepts a connection quickest, as
// PIA's scripts measure it; regions that don't answer within a second are out.
func (a API) Fastest(ctx context.Context, regions []Region, dial Dial) (Region, error) {
	type result struct {
		r  Region
		ms time.Duration
	}
	ch := make(chan result, len(regions))
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for _, r := range regions {
		go func() {
			if len(r.Servers.Meta) == 0 {
				ch <- result{r, -1}
				return
			}
			start := time.Now()
			c, err := dial(ctx, "tcp4", net.JoinHostPort(r.Servers.Meta[0].IP, strconv.Itoa(a.MetaPort)))
			if err != nil {
				ch <- result{r, -1}
				return
			}
			c.Close()
			ch <- result{r, time.Since(start)}
		}()
	}
	best := result{ms: -1}
	for range regions {
		if res := <-ch; res.ms >= 0 && (best.ms < 0 || res.ms < best.ms) {
			best = res
		}
	}
	if best.ms < 0 {
		return Region{}, errors.New("no PIA region answered")
	}
	return best.r, nil
}

// Key is a server's answer to addKey.
type Key struct {
	Status     string   `json:"status"`
	Message    string   `json:"message"`
	ServerKey  string   `json:"server_key"`
	ServerPort int      `json:"server_port"`
	ServerIP   string   `json:"server_ip"`
	ServerVIP  string   `json:"server_vip"` // the server inside the tunnel, for port forwarding
	PeerIP     string   `json:"peer_ip"`
	DNS        []string `json:"dns_servers"`
}

// client talks TLS to one of PIA's own servers: it connects to the server's IP
// and checks the certificate against PIA's CA and the server's name.
func (a API) client(dial Dial, ip string, srvName string) *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				_, port, _ := net.SplitHostPort(addr)
				return dial(ctx, "tcp4", net.JoinHostPort(ip, port))
			},
			TLSClientConfig: &tls.Config{RootCAs: a.CA, ServerName: srvName, MinVersion: tls.VersionTLS12},
		},
	}
}

// AddKey registers a WireGuard public key with a server.
func (a API) AddKey(ctx context.Context, dial Dial, srv Server, token, pub string) (Key, error) {
	u := url.URL{Scheme: "https", Host: net.JoinHostPort(srv.CN, strconv.Itoa(a.KeyPort)), Path: "/addKey",
		RawQuery: url.Values{"pt": {token}, "pubkey": {pub}}.Encode()}
	var k Key
	if err := a.get(ctx, a.client(dial, srv.IP, srv.CN), u.String(), &k); err != nil {
		return Key{}, err
	}
	if k.ServerKey == "" || k.PeerIP == "" || k.ServerPort == 0 {
		return Key{}, errors.New("PIA's server sent an incomplete key reply")
	}
	return k, nil
}

func (a API) get(ctx context.Context, c *http.Client, u string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		// The query holds the login token, and errors end up in the log.
		var ue *url.Error
		if errors.As(err, &ue) {
			if u, perr := url.Parse(ue.URL); perr == nil {
				u.RawQuery = ""
				ue.URL = u.String()
			}
		}
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var st struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return fmt.Errorf("PIA's server answered %s", resp.Status)
	}
	if st.Status != "OK" {
		msg := st.Message
		if msg == "" {
			msg = st.Status
		}
		return fmt.Errorf("PIA's server refused: %s", msg)
	}
	return json.Unmarshal(b, into)
}

// Conf is the WireGuard config for the key priv registered with srv.
func (k Key) Conf(priv string, srv Server) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nAddress = %s\n", priv, k.PeerIP)
	if len(k.DNS) > 0 {
		fmt.Fprintf(&b, "DNS = %s\n", strings.Join(k.DNS, ", "))
	}
	fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = %s\nPersistentKeepalive = 25\n",
		k.ServerKey, net.JoinHostPort(srv.IP, strconv.Itoa(k.ServerPort)))
	return b.String()
}

// Signature lets a port be bound on one server, until it expires (about two months).
type Signature struct {
	Payload   string // base64, sent back as received
	Signature string
	Port      int
	Expires   time.Time
}

// Signature asks the server for a forwarded port. gw is the server as seen
// inside the tunnel, and dial must go through that tunnel.
func (a API) Signature(ctx context.Context, dial Dial, gw Server, token string) (Signature, error) {
	u := url.URL{Scheme: "https", Host: net.JoinHostPort(gw.CN, strconv.Itoa(a.PFPort)), Path: "/getSignature",
		RawQuery: url.Values{"token": {token}}.Encode()}
	var r struct {
		Payload   string `json:"payload"`
		Signature string `json:"signature"`
	}
	if err := a.get(ctx, a.client(dial, gw.IP, gw.CN), u.String(), &r); err != nil {
		return Signature{}, err
	}
	raw, err := base64.StdEncoding.DecodeString(r.Payload)
	if err != nil {
		return Signature{}, fmt.Errorf("reading PIA's port payload: %w", err)
	}
	var p struct {
		Port    int       `json:"port"`
		Expires time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.Port <= 0 {
		return Signature{}, fmt.Errorf("reading PIA's port payload: %v", err)
	}
	return Signature{Payload: r.Payload, Signature: r.Signature, Port: p.Port, Expires: p.Expires}, nil
}

// BindPort (re)binds the signed port; PIA drops it unless this repeats every 15 minutes.
func (a API) BindPort(ctx context.Context, dial Dial, gw Server, s Signature) error {
	u := url.URL{Scheme: "https", Host: net.JoinHostPort(gw.CN, strconv.Itoa(a.PFPort)), Path: "/bindPort",
		RawQuery: url.Values{"payload": {s.Payload}, "signature": {s.Signature}}.Encode()}
	var r struct{}
	return a.get(ctx, a.client(dial, gw.IP, gw.CN), u.String(), &r)
}
