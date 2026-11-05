package vpn

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/vpn/vpntest"
)

func piaRig(t *testing.T, f *vpntest.PIA, ids ...string) *rig {
	t.Helper()
	return newRig(t, Options{PIA: f.API, PIARegion: "nl"}, ids...)
}

func TestPIALoginGivesEveryInterfaceATunnel(t *testing.T) {
	f := vpntest.NewPIA(t, false, 1)
	r := piaRig(t, f)
	r.tick(time.Now())
	if err := r.SetPIA("p0123456", "right"); err != nil {
		t.Fatal(err)
	}
	r.waitUp(t)
	if s := r.status(t, "lo", time.Now()); s.Provider != "pia" || s.State != "up" {
		t.Fatalf("PIA tunnel %+v", s)
	}
	if v := r.PIA(); v.State != "ok" || v.Region != "Netherlands" || !v.Configured {
		t.Fatalf("PIA shown as %+v", v)
	}
	// The login is kept encrypted.
	b, _ := os.ReadFile(filepath.Join(r.dir, "pia.dat"))
	if len(b) == 0 || bytes.Contains(b, []byte("right")) {
		t.Fatal("pia.dat is missing or holds the password in the clear")
	}
}

// PIA gives an account one peer IP per server, so two tunnels on one server
// steal it from each other and only the newest carries traffic.
func TestPIATunnelsOfTwoInterfacesEachCarryTraffic(t *testing.T) {
	f := vpntest.NewPIA(t, false, 2)
	r := piaRig(t, f, "lo", "lo2")
	r.tick(time.Now())
	if err := r.SetPIA("p0123456", "right"); err != nil {
		t.Fatal(err)
	}
	r.waitUp(t, "lo", "lo2")
	for _, id := range []string{"lo", "lo2"} {
		up, _ := r.Up(id)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodHead, "http://203.0.113.1/", nil)
		resp, err := up.Client.Do(req)
		cancel()
		if err != nil {
			t.Fatalf("the tunnel of %s carries no traffic: %v", id, err)
		}
		resp.Body.Close()
	}
}

func TestPIATunnelsComeBackAfterARestart(t *testing.T) {
	f := vpntest.NewPIA(t, false, 1)
	r := piaRig(t, f)
	r.tick(time.Now())
	if err := r.SetPIA("p0123456", "right"); err != nil {
		t.Fatal(err)
	}
	r.waitUp(t)
	r.Close()

	if p := r.open(t); len(p) != 0 {
		t.Fatalf("problems after the restart: %v", p)
	}
	r.waitUp(t)
	if f.Keys.Load() < 2 {
		t.Fatalf("after the restart %d keys were registered in all, want a fresh one", f.Keys.Load())
	}
}

func TestAnUnreadablePIALoginIsSetAside(t *testing.T) {
	r := newRig(t, Options{})
	os.WriteFile(filepath.Join(r.dir, "pia.dat"), []byte("not sealed"), 0o600)
	p := r.open(t)
	if len(p) != 1 || p[0].Iface != "" {
		t.Fatalf("problems %v, want the PIA login", p)
	}
	if r.PIA().Configured {
		t.Fatal("an unreadable login was loaded")
	}
	if _, err := os.Stat(filepath.Join(r.dir, "pia.dat.broken")); err != nil {
		t.Fatal(err)
	}
}

func TestRefusedPIALoginWaitsForTheUser(t *testing.T) {
	f := vpntest.NewPIA(t, false, 1)
	r := piaRig(t, f)
	r.tick(time.Now())
	if err := r.SetPIA("p0123456", "wrong"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the login to be refused", 10*time.Second, func() bool {
		r.tick(time.Now())
		return r.PIA().State == "login-failed"
	})
	n := f.Logins.Load()
	for range 3 {
		r.tick(time.Now().Add(time.Hour))
	}
	if f.Logins.Load() != n {
		t.Fatalf("kept logging in with a refused password: %d tries", f.Logins.Load())
	}
	f.Password = "wrong" // the user fixes it on PIA's side and saves again
	if err := r.SetPIA("p0123456", "wrong"); err != nil {
		t.Fatal(err)
	}
	r.waitUp(t)
}

func TestPIATunnelForwardsAPortAndBindsItEveryFifteenMinutes(t *testing.T) {
	f := vpntest.NewPIA(t, true, 1)
	r := piaRig(t, f)
	r.tick(time.Now())
	if err := r.SetPIA("p0123456", "right"); err != nil {
		t.Fatal(err)
	}
	r.waitUp(t) // asked for a port as soon as it is up
	start := time.Now()
	if s := r.status(t, "lo", start); s.Incoming != "reachable" {
		t.Fatalf("tunnel with a forwarded port shown as %q", s.Incoming)
	}
	if up, _ := r.Up("lo"); up.Port != 47000 {
		t.Fatalf("torrents would listen on %d, want 47000", up.Port)
	}

	// PIA drops a port that isn't bound again within 15 minutes.
	r.tick(start.Add(5 * time.Minute))
	if f.Binds.Load() != 1 {
		t.Fatalf("bound again after 5 minutes (%d binds)", f.Binds.Load())
	}
	for i := 1; i <= 5; i++ { // over an hour
		r.tick(start.Add(time.Duration(i) * 15 * time.Minute))
	}
	if f.Binds.Load() != 6 || f.Signatures.Load() != 1 {
		t.Fatalf("an hour later: %d binds, %d signatures; want a bind every 15 minutes on one signature", f.Binds.Load(), f.Signatures.Load())
	}
	at := start.Add(75 * time.Minute)
	if s := r.status(t, "lo", at); s.Incoming != "reachable" {
		t.Fatalf("after an hour the tunnel is %q", s.Incoming)
	}
	// Unbound for 20 minutes, it no longer counts as reachable.
	if s := r.status(t, "lo", at.Add(20*time.Minute)); s.Incoming == "reachable" {
		t.Fatal("still reachable 20 minutes after the last bind")
	}
}

func TestPIARegionWithoutForwardingIsOutgoingOnly(t *testing.T) {
	f := vpntest.NewPIA(t, false, 1)
	r := piaRig(t, f)
	r.tick(time.Now())
	if err := r.SetPIA("p0123456", "right"); err != nil {
		t.Fatal(err)
	}
	r.waitUp(t)
	r.tick(time.Now())
	if f.Signatures.Load() != 0 {
		t.Fatal("asked a region without port forwarding for a port")
	}
	if s := r.status(t, "lo", time.Now()); s.Incoming != "outgoing" {
		t.Fatalf("tunnel in a region without forwarding shown as %q", s.Incoming)
	}
	if up, ok := r.Up("lo"); !ok || up.Port != 0 {
		t.Fatalf("a tunnel without a forwarded port: up %v, port %d", ok, up.Port)
	}
}

func TestChangingThePIARegionReconnects(t *testing.T) {
	f := vpntest.NewPIA(t, false, 1)
	r := piaRig(t, f)
	r.tick(time.Now())
	if err := r.SetPIA("p0123456", "right"); err != nil {
		t.Fatal(err)
	}
	r.waitUp(t)
	first, _ := r.Up("lo")
	r.SetPIARegion("nl") // even the same one: the user asked to reconnect
	if _, ok := r.Up("lo"); ok {
		t.Fatal("the tunnel stayed up across a region change")
	}
	r.waitUp(t)
	if again, _ := r.Up("lo"); again.Gen == first.Gen || f.Keys.Load() < 2 {
		t.Fatalf("after the region change: generation %d (was %d), %d keys", again.Gen, first.Gen, f.Keys.Load())
	}
}
