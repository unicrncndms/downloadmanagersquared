package netif

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"
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
		resp, err := NewBinding(i).HTTPClient().Do(req)
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
