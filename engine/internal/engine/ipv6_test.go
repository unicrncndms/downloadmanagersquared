package engine

import (
	"bytes"
	"crypto/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/netif"
	"downloadmanagersquared/engine/internal/netif/netiftest"
	"downloadmanagersquared/engine/internal/paths"
)

// An IPv6-only connection is one more line: it and an IPv4 one fetch one file
// together, each over its own family, from a server with both.
func TestAnIPv6OnlyAndAnIPv4InterfaceDownloadOneFileTogether(t *testing.T) {
	lo := loopbackIfaces()[0]
	if lo.Index == 0 {
		t.Skip("no loopback interface")
	}
	port, _ := netiftest.DNS(t, net.IPv4(127, 0, 0, 1), net.IPv6loopback)
	old := netif.DNSPort
	netif.DNSPort = port
	t.Cleanup(func() { netif.DNSPort = old })
	v4, v6 := lo, lo
	v4.ID, v4.Name, v4.DNS = "a", "Ethernet", []net.IP{net.IPv4(127, 0, 0, 1)}
	v6.ID, v6.Name, v6.IPv4, v6.IPv6, v6.DNS = "b", "Phone", nil, net.IPv6loopback, []net.IP{net.IPv6loopback}

	data := make([]byte, 48<<20)
	rand.Read(data)
	var over4, over6 atomic.Int64
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		n := &over6
		if net.ParseIP(host).To4() != nil {
			n = &over4
		}
		w.Header().Set("Content-Disposition", `attachment; filename="both.bin"`)
		http.ServeContent(counted{w, n}, r, "both.bin", time.Time{}, bytes.NewReader(data))
	})}
	ln4, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := strconv.Itoa(ln4.Addr().(*net.TCPAddr).Port)
	ln6, err := net.Listen("tcp6", net.JoinHostPort("::1", p))
	if err != nil {
		t.Skip("no IPv6 loopback: ", err)
	}
	go srv.Serve(ln4)
	go srv.Serve(ln6)
	t.Cleanup(func() { srv.Close() })

	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Shutdown)
	e.settings.SaveDir = t.TempDir()
	e.settings.MainInterface = "a"
	e.settings.IPv6 = true
	e.mu.Lock()
	e.refreshInterfacesLocked([]netif.Interface{v4, v6})
	for _, id := range []string{"a", "b"} {
		e.settings.Interfaces[id].DownloadLimit = 8 << 20 // so the split has time to bring the second one in
	}
	e.applyLimitsLocked()
	e.mu.Unlock()

	id, err := e.Add(AddRequest{URL: "http://both.test:" + p + "/both.bin", VPN: "off"})
	if err != nil {
		t.Fatal(err)
	}
	d, _ := e.get(id)
	e.schedule()
	waitFor(t, "the download over both families", 60*time.Second, func() bool {
		return d.Status() == download.StatusCompleted || d.Status() == download.StatusFailed
	})
	if s := d.Summary(); s.Status != download.StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	got, err := os.ReadFile(filepath.Join(e.settings.SaveDir, "both.bin"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("file: %d bytes, %v", len(got), err)
	}
	t.Logf("IPv4 carried %d MiB, IPv6 %d MiB", over4.Load()>>20, over6.Load()>>20)
	if over4.Load() == 0 || over6.Load() == 0 {
		t.Fatalf("both should carry the file: IPv4 %d bytes, IPv6 %d", over4.Load(), over6.Load())
	}
	if f := d.Summary().Links["b"].Family; f != "IPv6" {
		t.Errorf("the IPv6-only link shows family %q", f)
	}
}

// The resolve proxy of an interface with IPv6 takes IPv6 targets through its
// binding; one without IPv6, and a tunnel (IPv4 inside), refuse them.
func TestTheResolveProxyHasIPv6OnlyWhereItsInterfaceHas(t *testing.T) {
	lo := loopbackIfaces()[0]
	v4, dual := lo, lo
	v4.ID, dual.ID, dual.IPv6 = "a", "b", net.IPv6loopback
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Shutdown)
	e.mu.Lock()
	e.refreshInterfacesLocked([]netif.Interface{v4, dual})
	e.mu.Unlock()
	if e.socksPath("a").IPv6() || !e.socksPath("b").IPv6() || e.socksPath(paths.Tunnel("b")).IPv6() {
		t.Errorf("IPv6: a %v, b %v, b's tunnel %v; want only b", e.socksPath("a").IPv6(), e.socksPath("b").IPv6(), e.socksPath(paths.Tunnel("b")).IPv6())
	}
}
