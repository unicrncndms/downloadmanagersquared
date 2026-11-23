package vpn

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/tunnel"
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

func TestInterfacesStartingTogetherShareOneLogin(t *testing.T) {
	f := vpntest.NewPIA(t, false, 3) // a server each
	r := piaRig(t, f, "lo", "wifi", "eth")
	r.tick(time.Now())
	if err := r.SetPIA("p0123456", "right"); err != nil {
		t.Fatal(err)
	}
	r.waitUp(t, "lo", "wifi", "eth")
	if n := f.Logins.Load(); n != 1 {
		t.Fatalf("%d logins for three interfaces starting together, want 1", n)
	}
}

func TestNoPIATunnelIsAddedAfterClose(t *testing.T) {
	f := vpntest.NewPIA(t, false, 1)
	r := piaRig(t, f)
	r.tick(time.Now())
	if err := r.SetPIA("p0123456", "right"); err != nil {
		t.Fatal(err)
	}
	r.waitUp(t)
	r.Close()
	r.Tick(time.Now(), r.ifaces) // a Tick that comes after Close must not make a tunnel (and its listener) again
	r.Wait()
	r.mu.Lock()
	n := len(r.tunnels)
	r.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d tunnels after Close, want none", n)
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

func TestAnImportedConfigWinsOverPIAAndRemovingItHandsBack(t *testing.T) {
	f := vpntest.NewPIA(t, false, 1)
	r := piaRig(t, f)
	r.tick(time.Now())
	if err := r.SetPIA("p0123456", "right"); err != nil {
		t.Fatal(err)
	}
	r.waitUp(t)

	priv, pub, _ := tunnel.GenerateKey()
	if err := r.Import("lo", vpntest.NewServer(t, pub).Conf(priv)); err != nil {
		t.Fatal(err)
	}
	r.waitUp(t)
	if s := r.status(t, "lo", time.Now()); s.Provider != "conf" {
		t.Fatalf("with an imported config the tunnel is %q's", s.Provider)
	}
	keys := f.Keys.Load()
	r.tick(time.Now().Add(time.Hour))
	if f.Keys.Load() != keys {
		t.Fatal("PIA registered a key for an interface that has its own config")
	}

	if err := r.Remove("lo"); err != nil {
		t.Fatal(err)
	}
	r.waitUp(t)
	if s := r.status(t, "lo", time.Now()); s.Provider != "pia" {
		t.Fatalf("after removing the config the tunnel is %q's, want PIA's again", s.Provider)
	}
}

func TestALostPIATunnelSaysNothingAboutExpiredConfigs(t *testing.T) {
	f := vpntest.NewPIA(t, false, 1)
	f.Unanswering = true
	r := piaRig(t, f)
	r.tick(time.Now())
	if err := r.SetPIA("p0123456", "right"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r.tick(now) // started, never answered
	r.tick(now.Add(tunnelGrace + time.Second))
	s := r.status(t, "lo", time.Now())
	if s.State != "down" || s.Err != "the VPN server doesn't answer" {
		t.Fatalf("lost PIA tunnel: %q, %q; want down, saying only that the server doesn't answer", s.State, s.Err)
	}
}

// Interfaces that start together measure the regions once.
func TestPIAStartsTogetherProbeTheRegionsOnce(t *testing.T) {
	f := vpntest.NewPIA(t, false, 2)
	r := newRig(t, Options{PIA: f.API}, "lo", "lo2")
	r.tick(time.Now())
	if err := r.SetPIA("p0123456", "right"); err != nil {
		t.Fatal(err)
	}
	r.waitUp(t, "lo", "lo2")
	if n := f.Probes.Load(); n != 1 {
		t.Fatalf("%d region probes, want 1", n)
	}
}

func TestRetriesStartAtFiveSecondsAndBackOffToThirty(t *testing.T) {
	old := jitter
	jitter = func() float64 { return 0.5 } // no jitter
	defer func() { jitter = old }()
	te := &entry{}
	var got []time.Duration
	for range 6 {
		got = append(got, te.nextRetry())
	}
	want := []time.Duration{5, 10, 20, 30, 30, 30}
	for i, w := range want {
		if got[i] != w*time.Second {
			t.Fatalf("retry delays %v, want %v seconds", got, want)
		}
	}
	jitter = func() float64 { return 1 }
	if d := (&entry{}).nextRetry(); d != 6*time.Second {
		t.Fatalf("first retry with the most jitter: %v", d)
	}
}

// A region with one server and two interfaces: the second tunnel waits rather
// than take the server from the first, which would then take it back.
func TestASecondTunnelWaitsRatherThanShareAServer(t *testing.T) {
	f := vpntest.NewPIA(t, false, 1)
	r := piaRig(t, f, "lo", "lo2")
	r.tick(time.Now())
	if err := r.SetPIA("p0123456", "right"); err != nil {
		t.Fatal(err)
	}
	var upID, waitID string
	waitFor(t, "one tunnel to come up", 30*time.Second, func() bool {
		r.tick(time.Now())
		for _, id := range []string{"lo", "lo2"} {
			if _, ok := r.Up(id); ok {
				upID, waitID = id, map[string]string{"lo": "lo2", "lo2": "lo"}[id]
				return true
			}
		}
		return false
	})
	first, _ := r.Up(upID)
	for i := range 3 { // retries come and go: the first tunnel keeps its server
		r.tick(time.Now().Add(time.Duration(i+1) * time.Minute))
	}
	if again, ok := r.Up(upID); !ok || again.Gen != first.Gen || f.Keys.Load() != 1 {
		t.Fatalf("the first tunnel: up %v, generation %d (was %d), %d keys registered", ok, again.Gen, first.Gen, f.Keys.Load())
	}
	if s := r.status(t, waitID, time.Now()); s.State != "down" || !strings.Contains(s.Err, "Pick another region") {
		t.Fatalf("the waiting tunnel shown as %+v", s)
	}
}

// An interface with a region of its own connects there, and leaves PIA's own
// region to the others.
func TestAnInterfaceWithItsOwnRegionConnectsThere(t *testing.T) {
	f := vpntest.NewPIA(t, false, 2)
	f.Germany = true // nl: one server, de: one server
	r := piaRig(t, f, "lo", "lo2")
	r.iface("lo2").PIARegion = "de"
	r.tick(time.Now())
	if err := r.SetPIA("p0123456", "right"); err != nil {
		t.Fatal(err)
	}
	r.waitUp(t, "lo", "lo2")
	if v := r.PIA(); v.Region != "Netherlands" {
		t.Fatalf("PIA's region shown as %q", v.Region)
	}
	lo, _ := r.Up("lo")
	r.SetPIARegion("nl") // PIA's own region reconnects only the tunnels that follow it
	if again, ok := r.Up("lo2"); !ok || f.Keys.Load() != 2 {
		t.Fatalf("lo2 after PIA's region changed: up %v, %+v, %d keys", ok, again, f.Keys.Load())
	}
	r.waitUp(t, "lo")
	r.iface("lo2").PIARegion = "" // now both want nl's one server: lo keeps it
	r.tick(time.Now())
	if _, ok := r.Up("lo2"); ok {
		t.Fatal("lo2 stayed up in Germany after its region was cleared")
	}
	for i := range 3 {
		r.tick(time.Now().Add(time.Duration(i+1) * time.Minute))
	}
	if again, ok := r.Up("lo"); !ok || again.Gen == lo.Gen {
		t.Fatalf("lo after PIA's region change and lo2 joining nl: up %v, generation %d (was %d)", ok, again.Gen, lo.Gen)
	}
	if s := r.status(t, "lo2", time.Now()); !strings.Contains(s.Err, "Netherlands") {
		t.Fatalf("lo2 waiting for nl's server shown as %+v", s)
	}
}
