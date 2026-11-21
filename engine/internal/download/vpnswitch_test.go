package download

import (
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/paths"
)

// tunnelEnv offers only the tunnel "tun" under VPN only, and both paths otherwise.
type tunnelEnv struct{ *testEnv }

func (e tunnelEnv) Paths(m paths.Mode) []Iface {
	all := e.testEnv.Paths(m)
	if m != paths.VPNOnly {
		return all
	}
	var out []Iface
	for _, i := range all {
		if i.ID == "tun" {
			out = append(out, i)
		}
	}
	return out
}

// Switching a running download to VPN only stops its requests on the plain
// connection at once, not at the next tick: nothing more goes outside the
// tunnel after the switch.
func TestSwitchingToVPNOnlyDropsPlainRequestsAtOnce(t *testing.T) {
	data := randomData(64 << 20)
	srv := fileServer(t, data, nil)
	defer srv.Close()
	base := &testEnv{ifaces: []Iface{iface("eth", 2, 20*time.Millisecond), iface("tun", 2, 20*time.Millisecond)}, main: "eth", finished: make(chan struct{})}
	d := New(tunnelEnv{base}, State{ID: "vs", URL: srv.URL + "/file/any", Dir: t.TempDir(), Size: -1, VPN: paths.Alongside})
	d.Start()
	defer d.Stop(StatusPaused)
	plain := func() (n int) {
		d.mu.Lock()
		defer d.mu.Unlock()
		for w := range d.workers {
			if w.iface == "eth" {
				n++
			}
		}
		return n
	}
	deadline := time.Now().Add(20 * time.Second)
	for plain() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no request on the plain connection: %+v", d.Summary())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := d.SetVPN(paths.VPNOnly); err != nil {
		t.Fatal(err)
	}
	// A tick is 500 ms: a cancelled request ends well before the next one.
	for end := time.Now().Add(150 * time.Millisecond); plain() > 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("%d requests still on the plain connection 150 ms after the switch", plain())
		}
	}
}
