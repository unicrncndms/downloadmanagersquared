package vpn

import (
	"os"
	"strings"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/tunnel"
	"downloadmanagersquared/engine/internal/vpn/vpntest"
)

func typedPort(t *testing.T, r *rig) int {
	t.Helper()
	for _, s := range r.Status(time.Now()) {
		if s.Iface == "lo" {
			return s.ForwardedPort
		}
	}
	return 0
}

func TestATypedInPortIsKeptWithItsTunnel(t *testing.T) {
	r := newRig(t, Options{})
	priv, pub, _ := tunnel.GenerateKey()
	conf := vpntest.NewServer(t, pub).Conf(priv)
	if err := r.Import("lo", conf); err != nil {
		t.Fatal(err)
	}
	if err := r.SetForwardedPort("lo", 41000); err != nil {
		t.Fatal(err)
	}
	r.Close()
	if p := r.open(t); len(p) != 0 || typedPort(t, r) != 41000 {
		t.Fatalf("after a restart: problems %v, port %d; want 41000", p, typedPort(t, r))
	}
	if err := r.Import("lo", conf); err != nil {
		t.Fatal(err)
	}
	if got := typedPort(t, r); got != 41000 {
		t.Fatalf("after importing the config again the port is %d, want 41000 kept", got)
	}
	if err := r.Remove("lo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.imported.optionsFile("lo")); err == nil {
		t.Fatal("the port's file outlived its tunnel")
	}
	if err := r.Import("lo", conf); err != nil {
		t.Fatal(err)
	}
	if got := typedPort(t, r); got != 0 {
		t.Fatalf("a tunnel imported after removal has port %d, want none", got)
	}
}

func TestForwardedPortsOutOfRangeWithoutATunnelOrForPIAAreRefused(t *testing.T) {
	f := vpntest.NewPIA(t, false, 1)
	r := piaRig(t, f, "lo", "lo2")
	if err := r.SetForwardedPort("lo", 41000); err == nil || !strings.Contains(err.Error(), "no imported VPN tunnel") {
		t.Fatalf("a port without a tunnel: %v", err)
	}
	priv, pub, _ := tunnel.GenerateKey()
	if err := r.Import("lo", vpntest.NewServer(t, pub).Conf(priv)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []int{-1, 65536} {
		if err := r.SetForwardedPort("lo", bad); err == nil {
			t.Fatalf("port %d accepted", bad)
		}
	}
	r.tick(time.Now())
	if err := r.SetPIA("p0123456", "right"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetForwardedPort("lo2", 41000); err == nil || !strings.Contains(err.Error(), "PIA") {
		t.Fatalf("a port for a PIA tunnel: %v", err)
	}
}

func TestAPortFromBeforeIsAdoptedOnlyByAnImportedTunnelWithoutOne(t *testing.T) {
	r := newRig(t, Options{})
	priv, pub, _ := tunnel.GenerateKey()
	if err := r.Import("lo", vpntest.NewServer(t, pub).Conf(priv)); err != nil {
		t.Fatal(err)
	}
	r.AdoptForwardedPort("wifi", 42000) // no tunnel there: dropped
	r.AdoptForwardedPort("lo", 41000)
	r.AdoptForwardedPort("lo", 43000) // it has one now
	if got := typedPort(t, r); got != 41000 {
		t.Fatalf("adopted port %d, want 41000", got)
	}
	r.Close()
	r.open(t)
	if got := typedPort(t, r); got != 41000 {
		t.Fatalf("after a restart the adopted port is %d, want 41000 kept", got)
	}
}
