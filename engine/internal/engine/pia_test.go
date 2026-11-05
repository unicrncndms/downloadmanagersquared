package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/vpn"
	"downloadmanagersquared/engine/internal/vpn/vpntest"
)

// piaEngine is a loopback engine whose PIA is f.
func piaEngine(t *testing.T, f *vpntest.PIA) *Engine {
	t.Helper()
	old := vpnOptions
	vpnOptions = func(o *vpn.Options) { o.PIA = f.API }
	t.Cleanup(func() { vpnOptions = old })
	return loopbackEngine(t)
}

func setPIA(t *testing.T, e *Engine, user, pass string) {
	t.Helper()
	p, _ := json.Marshal(map[string]string{"user": user, "pass": pass, "region": "nl"})
	if _, err := e.Call(nil, "setPia", p); err != nil {
		t.Fatal(err)
	}
}

func TestPIALoginGivesEveryInterfaceATunnel(t *testing.T) {
	e := piaEngine(t, vpntest.NewPIA(t, false, 1))
	setPIA(t, e, "p0123456", "right")
	waitTunnelUp(t, e)
	if v := ifaceView(t, e, paths.Tunnel("lo")); !v.Present || v.Name != "VPN via Loopback" || v.Provider != "pia" {
		t.Fatalf("PIA tunnel shown as %+v", v)
	}
	if s := e.Snapshot().PIA; s.State != "ok" || s.Region != "Netherlands" {
		t.Fatalf("PIA shown as %+v", s)
	}
	// The login stays in the engine; the extension never gets it back.
	st, _ := json.Marshal(e.Snapshot())
	set, _ := json.Marshal(e.settings)
	for _, leak := range [][]byte{st, set} {
		if bytes.Contains(leak, []byte(`right`)) || bytes.Contains(leak, []byte("p0123456")) {
			t.Fatalf("the PIA login reaches the extension: %s", leak)
		}
	}
	for _, u := range e.Paths(paths.VPNOnly) {
		if u.ID == paths.Tunnel("lo") {
			return
		}
	}
	t.Fatal("the PIA tunnel isn't a path for VPN-only downloads")
}

func TestPIATunnelForwardsAPortAndTakesPeersOnIt(t *testing.T) {
	f := vpntest.NewPIA(t, true, 1)
	e := piaEngine(t, f)
	setPIA(t, e, "p0123456", "right")
	waitTunnelUp(t, e)
	if v := ifaceView(t, e, paths.Tunnel("lo")); v.Incoming != "reachable" {
		t.Fatalf("tunnel with a forwarded port shown as %q", v.Incoming)
	}
	// The VPN-only session listens on the forwarded port inside the tunnel,
	// where PIA sends incoming peers, while one of its torrents is in the swarm.
	e.syncTorrentIfaces()
	vpnTorrentInSwarm(t, e)
	if ifs := e.torrentIfacesFor(paths.VPNOnly); len(ifs) != 1 || ifs[0].Port != 47000 {
		t.Fatalf("VPN-only torrents listen on %+v, want port 47000", ifs)
	}
	c, err := f.VPN.T.DialContext(t.Context(), "tcp4", "10.9.0.2:47000")
	if err != nil {
		t.Fatalf("a peer coming in through PIA couldn't connect: %v", err)
	}
	c.Close()
}

func vpnTorrentInSwarm(t *testing.T, e *Engine) {
	t.Helper()
	tor, err := e.session(paths.VPNOnly).Add(torrentState(fmt.Sprintf("magnet:?xt=urn:btih:%040x", 77), paths.VPNOnly, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	tor.Start()
	t.Cleanup(func() { tor.Stop(download.StatusPaused) })
	waitFor(t, "the VPN-only session to go live", 5*time.Second, e.session(paths.VPNOnly).Live)
}
