package engine

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/tunnel"
)

func setForwarded(e *Engine, port int) error {
	p, _ := json.Marshal(map[string]any{"iface": "lo", "port": port})
	_, err := e.Call(nil, "setForwardedPort", p)
	return err
}

func TestATypedInPortIsListenedOnInsideTheTunnelWithoutNATPMP(t *testing.T) {
	e, srv, f := pmpTunnel(t) // its gateway would answer NAT-PMP: the typed-in port wins
	if err := setForwarded(e, 41000); err != nil {
		t.Fatal(err)
	}
	e.forwardPMP(time.Now()).Wait()
	if tcp, udp, probes := f.counts(); tcp+udp+probes != 0 {
		t.Fatalf("NAT-PMP asked (TCP %d, UDP %d, probes %d) despite a typed-in port", tcp, udp, probes)
	}
	v := ifaceView(t, e, paths.Tunnel("lo"))
	if v.Incoming != "forwarded" || v.ForwardedPort != 41000 {
		t.Fatalf("tunnel shown as incoming %q, port %d; want forwarded, 41000", v.Incoming, v.ForwardedPort)
	}
	e.syncTorrentIfaces()
	vpnTorrentInSwarm(t, e)
	if ifs := e.torrentIfacesFor(paths.VPNOnly); len(ifs) != 1 || ifs[0].Port != 41000 {
		t.Fatalf("VPN-only torrents listen on %+v, want port 41000", ifs)
	}
	c, err := srv.t.DialContext(t.Context(), "tcp4", "10.9.0.2:41000")
	if err != nil {
		t.Fatalf("a peer coming in through the VPN server couldn't connect: %v", err)
	}
	c.Close()
}

func TestClearingTheTypedInPortHandsBackToNATPMP(t *testing.T) {
	e, _, _ := pmpTunnel(t)
	if err := setForwarded(e, 41000); err != nil {
		t.Fatal(err)
	}
	if err := setForwarded(e, 0); err != nil {
		t.Fatal(err)
	}
	e.forwardPMP(time.Now()).Wait()
	if v := ifaceView(t, e, paths.Tunnel("lo")); v.Incoming != "reachable" || v.ForwardedPort != 0 {
		t.Fatalf("after clearing the port the tunnel shows %q, port %d; want reachable by NAT-PMP", v.Incoming, v.ForwardedPort)
	}
	if ifs := e.torrentIfacesFor(paths.VPNOnly); len(ifs) != 1 || ifs[0].Port != 51234 {
		t.Fatalf("VPN-only torrents listen on %+v, want NAT-PMP's 51234", ifs)
	}
}

func savedForwardedPort(t *testing.T, e *Engine) int {
	t.Helper()
	s, _ := loadSettings(filepath.Join(e.dir, "settings.json"))
	if is := s.Interfaces["lo"]; is != nil {
		return is.ForwardedPort
	}
	return 0
}

func TestATypedInPortIsSavedKeptOnReimportAndGoneWithTheTunnel(t *testing.T) {
	e := loopbackEngine(t)
	clientPriv, clientPub, _ := tunnel.GenerateKey()
	srv := newVPNServer(t, clientPub)
	importConf(t, e, srv, clientPriv)
	if err := setForwarded(e, 41000); err != nil {
		t.Fatal(err)
	}
	if got := savedForwardedPort(t, e); got != 41000 {
		t.Fatalf("saved port %d, want 41000", got)
	}
	importConf(t, e, srv, clientPriv)
	if got := savedForwardedPort(t, e); got != 41000 {
		t.Fatalf("after importing the config again the port is %d, want 41000 kept", got)
	}
	p, _ := json.Marshal(map[string]string{"iface": "lo"})
	if _, err := e.Call(nil, "removeTunnel", p); err != nil {
		t.Fatal(err)
	}
	if got := savedForwardedPort(t, e); got != 0 {
		t.Fatalf("after removing the tunnel the port is %d, want none", got)
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
	e.mu.Lock()
	e.tunnels["lo"].pia = true
	e.mu.Unlock()
	if err := setForwarded(e, 41000); err == nil || !strings.Contains(err.Error(), "PIA") {
		t.Fatalf("a port for a PIA tunnel: %v", err)
	}
}
