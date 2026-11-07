package download

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// clientTo is an interface's client that connects to ip whatever the URL's
// host: a stand-in for a binding resolving the name to that address.
func clientTo(ip string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, _ := net.SplitHostPort(addr)
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(ip, port))
	}}}
}

// raceClient connects its first connection to ::1 and the rest to 127.0.0.1:
// Happy Eyeballs, with IPv6 winning the probe's race and IPv4 every later one.
func raceClient() *http.Client {
	var n atomic.Int32
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, _ := net.SplitHostPort(addr)
		ip := "127.0.0.1"
		if n.Add(1) == 1 {
			ip = "::1"
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(ip, port))
	}}}
}

// A dual-stack interface's link keeps the family its probe connected over:
// every chunk asks over that family, and the family is saved and shown.
func TestALinkKeepsTheFamilyItsProbeGot(t *testing.T) {
	data := randomData(8 << 20)
	var mu sync.Mutex
	families := map[string]int{} // of the data requests
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if r.Header.Get("Range") != "bytes=0-0" {
			f := "IPv6"
			if net.ParseIP(host).To4() != nil {
				f = "IPv4"
			}
			mu.Lock()
			families[f]++
			mu.Unlock()
		}
		http.ServeContent(w, r, "dual.bin", time.Time{}, bytes.NewReader(data))
	})}
	ln4, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(ln4.Addr().(*net.TCPAddr).Port)
	ln6, err := net.Listen("tcp6", net.JoinHostPort("::1", port))
	if err != nil {
		t.Skip("no IPv6 loopback: ", err)
	}
	go srv.Serve(ln4)
	go srv.Serve(ln6)
	defer srv.Close()

	// The probe gets IPv6; the probe's client would give the chunks IPv4,
	// so only the link's family keeps them on IPv6.
	dual := Iface{ID: "dual", Conns: 4, Client: raceClient(),
		ByFamily: map[string]*http.Client{"IPv4": clientTo("127.0.0.1"), "IPv6": clientTo("::1")}}
	env := &testEnv{ifaces: []Iface{dual}, main: "dual"}
	dir := t.TempDir()
	d := runToEnd(t, env, State{ID: "fam", URL: "http://dual.test:" + port + "/dual.bin", Dir: dir, Size: -1, Status: StatusQueued})
	checkFile(t, filepath.Join(dir, "dual.bin"), data)
	if families["IPv6"] == 0 || families["IPv4"] != 0 {
		t.Errorf("data requests by family %v, want IPv6 only", families)
	}
	if f := d.State().Links["dual"].Family; f != "IPv6" {
		t.Errorf("saved family %q", f)
	}
	if f := d.Summary().Links["dual"].Family; f != "IPv6" {
		t.Errorf("shown family %q", f)
	}

	// An interface without IPv6 (no family clients) keeps none: nothing changes with IPv6 off.
	env = &testEnv{ifaces: []Iface{{ID: "eth", Conns: 2, Client: clientTo("127.0.0.1")}}, main: "eth"}
	d = runToEnd(t, env, State{ID: "v4", URL: "http://v4.test:" + port + "/dual.bin", Dir: t.TempDir(), Size: -1, Status: StatusQueued})
	if f := d.State().Links["eth"].Family; f != "" {
		t.Errorf("an IPv4-only interface saved family %q", f)
	}
}
