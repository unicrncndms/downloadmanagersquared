package netif

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/rate"
)

// Lists this machine's interfaces and fetches the public IP through each.
// Needs network access; skipped with -short.
func TestListAndBind(t *testing.T) {
	if testing.Short() {
		t.Skip("needs network")
	}
	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range list {
		t.Logf("%s | %s | ip=%s gw=%s dns=%v metered=%v vpn=%v default=%v", i.Name, i.Description, i.IPv4, i.Gateway, i.DNS, i.Metered, i.VPN, i.IsDefault)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org", nil)
		resp, err := NewBinding(i, rate.NewShaper()).HTTPClient().Do(req)
		cancel()
		if err != nil {
			t.Logf("  public IP: error %v", err)
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Logf("  public IP via this interface: %s", b)
	}
}

// loopback is this machine's loopback interface, as List would describe it.
func loopback(t *testing.T) Interface {
	t.Helper()
	ifs, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 {
			return Interface{ID: "lo", Name: i.Name, Index: uint32(i.Index), IPv4: net.IPv4(127, 0, 0, 1)}
		}
	}
	t.Skip("no loopback interface")
	return Interface{}
}

func TestBindingHoldsHTTPToItsShapersLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 300<<10))
	}))
	defer srv.Close()
	s := rate.NewShaper()
	s.SetLimits(0, 200<<10)
	b := NewBinding(loopback(t), s)
	defer b.Close()
	start := time.Now()
	resp, err := b.HTTPClient().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if took := time.Since(start); n != 300<<10 || took < time.Second {
		t.Fatalf("300 KB at 200 KB/s: got %d bytes in %v", n, took)
	}
}

func TestBindingHoldsPacketsToItsShapersLimit(t *testing.T) {
	s := rate.NewShaper()
	s.SetLimits(100<<10, 0)
	pc, err := NewBinding(loopback(t), s).ListenPacket(0)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	start := time.Now()
	for range 200 {
		pc.WriteTo(make([]byte, 1000), pc.LocalAddr())
	}
	if took := time.Since(start); took < 1500*time.Millisecond {
		t.Fatalf("200 KB of packets at 100 KB/s took %v", took)
	}
}
