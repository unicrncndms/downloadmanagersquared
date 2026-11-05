package engine

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/ipc"
	"downloadmanagersquared/engine/internal/netif"
)

// twoLoopbackIfaces are two interfaces on this machine's loopback, at 127.0.0.1
// and 127.0.0.2: two source addresses a server can tell apart, as it would two
// real connections.
func twoLoopbackIfaces(t *testing.T) []netif.Interface {
	t.Helper()
	lo := loopbackIfaces()[0]
	if lo.Index == 0 {
		t.Skip("no loopback interface")
	}
	a, b := lo, lo
	a.ID, a.Name, a.IPv4 = "a", "Ethernet", net.IPv4(127, 0, 0, 1)
	b.ID, b.Name, b.IPv4 = "b", "Phone", net.IPv4(127, 0, 0, 2)
	return []netif.Interface{a, b}
}

// ipBoundHoster serves data at /file/<ip> only to connections from that IP and
// a web page to every other one, like a hoster that binds each generated link
// to the address that asked for it. It counts the bytes each source address got.
type ipBoundHoster struct {
	*httptest.Server
	mu   sync.Mutex
	sent map[string]*atomic.Int64
}

func newIPBoundHoster(t *testing.T, data []byte) *ipBoundHoster {
	h := &ipBoundHoster{sent: map[string]*atomic.Int64{}}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		from, _, _ := net.SplitHostPort(r.RemoteAddr)
		if r.URL.Path == "/page" || strings.TrimPrefix(r.URL.Path, "/file/") != from {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<html>download page</html>")
			return
		}
		h.mu.Lock()
		n := h.sent[from]
		if n == nil {
			n = &atomic.Int64{}
			h.sent[from] = n
		}
		h.mu.Unlock()
		w.Header().Set("Content-Disposition", `attachment; filename="split.bin"`)
		http.ServeContent(counted{w, n}, r, "split.bin", time.Time{}, bytes.NewReader(data))
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *ipBoundHoster) bytesTo(ip string) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n := h.sent[ip]; n != nil {
		return n.Load()
	}
	return 0
}

type counted struct {
	http.ResponseWriter
	n *atomic.Int64
}

func (c counted) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// resolvingExtension is an extension that answers every resolve the way the real
// one does once the landing page gave it a link: with the link for that interface.
func resolvingExtension(t *testing.T, e *Engine, link func(iface string) string) *atomic.Int32 {
	t.Helper()
	ours, theirs := net.Pipe()
	t.Cleanup(func() { ours.Close(); theirs.Close() })
	conn := ipc.NewConn(ours)
	var resolves atomic.Int32
	go func() {
		r := bufio.NewReader(theirs)
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				return
			}
			var ev struct {
				Event string     `json:"event"`
				Data  resolveReq `json:"data"`
			}
			if json.Unmarshal(line, &ev) != nil || ev.Event != "resolve" {
				continue
			}
			resolves.Add(1)
			p, _ := json.Marshal(map[string]string{"id": ev.Data.ID, "iface": ev.Data.Iface, "url": link(ev.Data.Iface)})
			go e.Call(conn, "resolved", p)
		}
	}()
	e.Connected(conn)
	params, _ := json.Marshal(helloParams{UserAgent: "UA", Browser: fakeBrowserExe(t, "firefox.exe")})
	if _, err := e.Call(conn, "hello", params); err != nil {
		t.Fatal(err)
	}
	return &resolves
}

// The point of DMS²: one file over two connections at once, through the engine's
// own bindings. The hoster tells the connections apart by their real source
// address, not by anything the test adds, so a binding that sent everything out
// one address would get the second connection only web pages and fail here.
func TestOneFileComesOverBothInterfacesThroughTheirOwnAddresses(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Shutdown)
	e.settings.SaveDir = t.TempDir()
	e.settings.MainInterface = "a"
	e.mu.Lock()
	e.refreshInterfacesLocked(twoLoopbackIfaces(t))
	for _, id := range []string{"a", "b"} {
		// Slowed to 8 MB/s each, so the split has time to bring the second one in.
		e.settings.Interfaces[id].DownloadLimit = 8 << 20
	}
	e.applyLimitsLocked()
	e.mu.Unlock()

	data := make([]byte, 48<<20)
	rand.Read(data)
	h := newIPBoundHoster(t, data)
	resolves := resolvingExtension(t, e, func(iface string) string {
		return h.URL + "/file/" + e.ifaceIPv4(iface)
	})

	id, err := e.Add(AddRequest{URL: h.URL + "/file/127.0.0.1", PageURL: h.URL + "/page", VPN: "off"})
	if err != nil {
		t.Fatal(err)
	}
	d, _ := e.get(id)
	e.schedule()
	waitFor(t, "the download over both interfaces", 60*time.Second, func() bool {
		return d.Status() == download.StatusCompleted || d.Status() == download.StatusFailed
	})
	if s := d.Summary(); s.Status != download.StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	got, err := os.ReadFile(filepath.Join(e.settings.SaveDir, "split.bin"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("file: %d bytes, %v; want the %d bytes served", len(got), err, len(data))
	}
	viaA, viaB := h.bytesTo("127.0.0.1"), h.bytesTo("127.0.0.2")
	t.Logf("127.0.0.1 got %d MiB, 127.0.0.2 got %d MiB, %d resolve(s)", viaA>>20, viaB>>20, resolves.Load())
	if viaA == 0 || viaB == 0 {
		t.Fatalf("both interfaces should carry the file: 127.0.0.1 got %d bytes, 127.0.0.2 got %d", viaA, viaB)
	}
	if n := resolves.Load(); n != 1 {
		t.Errorf("%d resolves, want one for the second interface's own link", n)
	}
}

// ifaceIPv4 is the address of interface id, as the hoster sees its connections.
func (e *Engine) ifaceIPv4(id string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ent := e.ifaces[id]; ent != nil {
		return ent.info.IPv4.String()
	}
	return ""
}
