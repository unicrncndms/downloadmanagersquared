package engine

import (
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/tunnel"
	"downloadmanagersquared/engine/internal/vpn"
	"downloadmanagersquared/engine/internal/vpn/vpntest"
)

// pmpTunnel is an engine with an imported tunnel to a server whose NAT-PMP
// answers. Before it comes up, before is called (nil: none).
func pmpTunnel(t *testing.T, before func(e *Engine)) (*Engine, *vpntest.Server, *vpntest.PMP) {
	t.Helper()
	old := vpnOptions
	vpnOptions = func(o *vpn.Options) { o.GatewayOK = vpntest.AnyGateway }
	t.Cleanup(func() { vpnOptions = old })
	e := loopbackEngine(t)
	clientPriv, clientPub, _ := tunnel.GenerateKey()
	srv := newVPNServer(t, clientPub)
	f := vpntest.ServePMP(t, srv)
	importConf(t, e, srv, clientPriv)
	if before != nil {
		before(e)
	}
	waitTunnelUp(t, e)
	return e, srv, f
}

func TestImportedTunnelForwardsAPortByNATPMPAndTakesPeersOnIt(t *testing.T) {
	e, srv, _ := pmpTunnel(t, nil)
	if v := ifaceView(t, e, paths.Tunnel("lo")); v.Incoming != "reachable" {
		t.Fatalf("tunnel with a NAT-PMP port shown as %q", v.Incoming)
	}
	e.syncTorrentIfaces()
	vpnTorrentInSwarm(t, e)
	if ifs := e.torrentIfacesFor(paths.VPNOnly); len(ifs) != 1 || ifs[0].Port != 51234 {
		t.Fatalf("VPN-only torrents listen on %+v, want port 51234", ifs)
	}
	c, err := srv.T.DialContext(t.Context(), "tcp4", "10.9.0.2:51234")
	if err != nil {
		t.Fatalf("a peer coming in through the VPN server couldn't connect: %v", err)
	}
	c.Close()
}

func TestChangedNATPMPPortMovesTheListener(t *testing.T) {
	e, srv, f := pmpTunnel(t, nil)
	e.syncTorrentIfaces()
	vpnTorrentInSwarm(t, e)
	f.Set(func(f *vpntest.PMP) { f.Public = 52000 })
	e.tickVPN(time.Now().Add(30 * time.Second))
	e.vpn.Wait()
	e.syncTorrentIfaces()
	if ifs := e.torrentIfacesFor(paths.VPNOnly); len(ifs) != 1 || ifs[0].Port != 52000 {
		t.Fatalf("after the port changed torrents listen on %+v, want 52000", ifs)
	}
	waitFor(t, "a peer to connect on the new port", 5*time.Second, func() bool {
		c, err := srv.T.DialContext(t.Context(), "tcp4", "10.9.0.2:52000")
		if err == nil {
			c.Close()
		}
		return err == nil
	})
}
