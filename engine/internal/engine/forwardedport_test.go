package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/tunnel"
	"downloadmanagersquared/engine/internal/vpn/vpntest"
)

func setForwarded(e *Engine, port int) error {
	p, _ := json.Marshal(map[string]any{"iface": "lo", "port": port})
	_, err := e.Call(nil, "setForwardedPort", p)
	return err
}

func TestATypedInPortIsListenedOnInsideTheTunnel(t *testing.T) {
	e, srv, _ := pmpTunnel(t, func(e *Engine) {
		if err := setForwarded(e, 41000); err != nil {
			t.Fatal(err)
		}
	})
	v := ifaceView(t, e, paths.Tunnel("lo"))
	if v.Incoming != "forwarded" || v.ForwardedPort != 41000 {
		t.Fatalf("tunnel shown as incoming %q, port %d; want forwarded, 41000", v.Incoming, v.ForwardedPort)
	}
	e.syncTorrentIfaces()
	vpnTorrentInSwarm(t, e)
	if ifs := e.torrentIfacesFor(paths.VPNOnly); len(ifs) != 1 || ifs[0].Port != 41000 {
		t.Fatalf("VPN-only torrents listen on %+v, want port 41000", ifs)
	}
	c, err := srv.T.DialContext(t.Context(), "tcp4", "10.9.0.2:41000")
	if err != nil {
		t.Fatalf("a peer coming in through the VPN server couldn't connect: %v", err)
	}
	c.Close()
}

func TestATypedInPortIsKeptAcrossARestartAndGoneWithTheTunnel(t *testing.T) {
	e := loopbackEngine(t)
	clientPriv, clientPub, _ := tunnel.GenerateKey()
	importConf(t, e, newVPNServer(t, clientPub), clientPriv)
	if err := setForwarded(e, 41000); err != nil {
		t.Fatal(err)
	}
	dir := e.dir
	e.Shutdown()
	e, err := open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	if st, _ := e.tunnelStatus("lo"); st.ForwardedPort != 41000 {
		t.Fatalf("after a restart the port is %d, want 41000", st.ForwardedPort)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "settings.json")); strings.Contains(string(b), "forwardedPort") {
		t.Fatalf("the port is in the settings: %s", b)
	}
	p, _ := json.Marshal(map[string]string{"iface": "lo"})
	if _, err := e.Call(nil, "removeTunnel", p); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.tunnelStatus("lo"); ok {
		t.Fatal("the tunnel is still there")
	}
}

// Before ports were kept with their tunnels, settings.json held them.
func TestAPortInOldSettingsMovesToItsTunnel(t *testing.T) {
	dir := t.TempDir()
	e, err := open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	clientPriv, clientPub, _ := tunnel.GenerateKey()
	if err := e.vpn.Import("lo", newVPNServer(t, clientPub).Conf(clientPriv)); err != nil {
		t.Fatal(err)
	}
	e.Shutdown()
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"interfaces":{"lo":{"name":"Loopback","forwardedPort":41000},"wifi":{"forwardedPort":42000}}}`), 0o600)

	e, err = open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	if st, _ := e.tunnelStatus("lo"); st.ForwardedPort != 41000 {
		t.Fatalf("the old port moved as %d, want 41000", st.ForwardedPort)
	}
	if _, ok := e.tunnelStatus("wifi"); ok {
		t.Fatal("a port without a tunnel made one")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "settings.json")); strings.Contains(string(b), "forwardedPort") || !strings.Contains(string(b), "Loopback") {
		t.Fatalf("settings after the move: %s", b)
	}
}

func TestForwardedPortsOutOfRangeOrWithoutATunnelAreRefused(t *testing.T) {
	e := loopbackEngine(t)
	if err := setForwarded(e, 41000); err == nil || !strings.Contains(err.Error(), "no imported VPN tunnel") {
		t.Fatalf("a port without a tunnel: %v", err)
	}
	clientPriv, clientPub, _ := tunnel.GenerateKey()
	importConf(t, e, newVPNServer(t, clientPub), clientPriv)
	for _, bad := range []int{-1, 65536} {
		if err := setForwarded(e, bad); err == nil {
			t.Fatalf("port %d accepted", bad)
		}
	}
}

func TestAPIATunnelTakesNoTypedInPort(t *testing.T) {
	e := piaEngine(t, vpntest.NewPIA(t, false, 1))
	setPIA(t, e, "p0123456", "right")
	waitTunnelUp(t, e)
	if err := setForwarded(e, 41000); err == nil || !strings.Contains(err.Error(), "PIA") {
		t.Fatalf("a port for a PIA tunnel: %v", err)
	}
}
