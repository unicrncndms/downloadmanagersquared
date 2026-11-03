package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"

	"downloadmanagersquared/engine/internal/atomicfile"
	"downloadmanagersquared/engine/internal/netif"
	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/pia"
	"downloadmanagersquared/engine/internal/tunnel"
	"downloadmanagersquared/engine/internal/winutil"
)

const (
	piaTokenLife   = 20 * time.Hour // PIA's tokens last 24
	piaRegionsLife = 6 * time.Hour
)

// piaLogin is what pia.dat holds, encrypted with DPAPI.
type piaLogin struct {
	User string `json:"user"`
	Pass string `json:"pass"`
}

// piaState is PIA as the provider of every interface's tunnel that has no
// .conf of its own. nil in Engine.pia means it isn't set up.
type piaState struct {
	login     piaLogin
	refused   bool // the login was refused: wait for the user to save it again
	err       string
	token     string
	tokenAt   time.Time
	regions   []pia.Region
	regionsAt time.Time
	region    pia.Region // the one in use
}

// PIAView is PIA as the settings page shows it; never the login itself.
type PIAView struct {
	Configured bool   `json:"configured"`
	State      string `json:"state"` // "off", "connecting", "ok", "login-failed", "error"
	Error      string `json:"error,omitempty"`
	Region     string `json:"region,omitempty"`
}

func (e *Engine) piaFile() string { return filepath.Join(e.dir, "pia.dat") }

func (e *Engine) loadPIA() {
	b, err := os.ReadFile(e.piaFile())
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var l piaLogin
	if err == nil {
		var plain []byte
		if plain, err = winutil.Unprotect(b); err == nil {
			err = json.Unmarshal(plain, &l)
		}
	}
	if err == nil && l.User == "" {
		err = errors.New("no user name")
	}
	if err != nil {
		log.Printf("PIA login unreadable, set aside: %v", err)
		setAside(e.piaFile())
		notify("PIA login unreadable", "PIA login couldn't be read, sign in again.", nil)
		return
	}
	e.pia = &piaState{login: l}
}

// setPIA saves a new login (or with user "" forgets PIA), and gives every
// interface without a .conf of its own a PIA tunnel.
func (e *Engine) setPIA(user, pass string) error {
	if user == "" {
		e.mu.Lock()
		e.pia = nil
		var ids []string
		for id, te := range e.tunnels {
			if te.pia {
				ids = append(ids, id)
			}
		}
		e.mu.Unlock()
		for _, id := range ids {
			e.removeTunnelEntry(id)
		}
		if err := os.Remove(e.piaFile()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	plain, _ := json.Marshal(piaLogin{user, pass})
	sealed, err := winutil.Protect(plain)
	if err != nil {
		return err
	}
	if err := atomicfile.Write(e.piaFile(), sealed, 0o600); err != nil {
		return err
	}
	e.mu.Lock()
	e.pia = &piaState{login: piaLogin{user, pass}}
	for _, te := range e.tunnels {
		if te.pia {
			te.retryAt = time.Time{} // try the new login at once
		}
	}
	e.mu.Unlock()
	e.syncTunnels(time.Now())
	return nil
}

// piaWantedLocked are the usable interfaces that should get a PIA tunnel:
// those without a tunnel of their own.
func (e *Engine) piaWantedLocked() []string {
	if e.pia == nil || e.pia.refused {
		return nil
	}
	var ids []string
	for id, ent := range e.ifaces {
		if ent.present && e.enabledLocked(ent) && e.tunnels[id] == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// piaLink is what a PIA tunnel needs for port forwarding.
type piaLink struct {
	forwards bool         // the region forwards ports
	gateways []pia.Server // where port forwarding answers inside the tunnel, best first
}

// piaConfig registers a new key for one interface, through that interface,
// and returns its tunnel config.
func (e *Engine) piaConfig(te *tunnelEntry, b *netif.Binding) (tunnel.Config, piaLink, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	e.mu.Lock()
	p, api, regionID := e.pia, e.piaAPI, e.settings.PIARegion
	e.mu.Unlock()
	if p == nil {
		return tunnel.Config{}, piaLink{}, errors.New("PIA isn't set up")
	}
	token, region, err := e.piaSession(ctx, p, api, regionID, b)
	if err != nil {
		return tunnel.Config{}, piaLink{}, err
	}
	priv, pub, err := tunnel.GenerateKey()
	if err != nil {
		return tunnel.Config{}, piaLink{}, err
	}
	srv, err := e.reservePIAServer(te, region.WG())
	if err != nil {
		return tunnel.Config{}, piaLink{}, err
	}
	k, err := api.AddKey(ctx, b.DialContext, srv, token, pub)
	if err != nil {
		e.mu.Lock()
		p.token = "" // maybe it expired early: log in again next time
		e.mu.Unlock()
		return tunnel.Config{}, piaLink{}, err
	}
	cfg, err := tunnel.ParseConf(k.Conf(priv, srv))
	// PIA's README puts port forwarding on the server's address inside the
	// tunnel; its scripts use the server's own. Try both.
	link := piaLink{forwards: region.PortForward}
	if k.ServerVIP != "" {
		link.gateways = append(link.gateways, pia.Server{IP: k.ServerVIP, CN: srv.CN})
	}
	link.gateways = append(link.gateways, srv)
	return cfg, link, err
}

// reservePIAServer picks te's server among a region's, the one fewest other
// tunnels hold. PIA gives an account one peer IP per server and routes it to
// the newest key, so two tunnels sharing a server would leave one of them dead.
// Apart, each also gets its own exit IP.
func (e *Engine) reservePIAServer(te *tunnelEntry, servers []pia.Server) (pia.Server, error) {
	if len(servers) == 0 {
		return pia.Server{}, errors.New("PIA's region has no WireGuard server")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	held := map[string]int{}
	for _, o := range e.tunnels {
		if o != te && o.pia && o.server != "" {
			held[o.server]++
		}
	}
	best := servers[0]
	for _, s := range servers[1:] {
		if held[s.CN] < held[best.CN] {
			best = s
		}
	}
	if held[best.CN] > 0 {
		log.Printf("PIA: more tunnels than servers in the region; %s is shared and only one of its tunnels will carry traffic", best.CN)
	}
	te.server = best.CN
	return best, nil
}

const (
	pfRebind = 15 * time.Minute // PIA drops a port not bound again within this
	pfRetry  = time.Minute
	pfShown  = 20 * time.Minute // a bind older than this no longer counts as reachable
)

// forwardLoop keeps the PIA tunnels' ports forwarded.
func (e *Engine) forwardLoop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-e.quit:
			return
		case now := <-t.C:
			e.forwardPorts(now)
		}
	}
}

// forwardPorts gets each PIA tunnel in a forwarding region a port, and binds
// it again every 15 minutes.
func (e *Engine) forwardPorts(now time.Time) {
	type job struct {
		id string
		te *tunnelEntry
		t  *tunnel.Tunnel
		b  *netif.Binding
	}
	var jobs []job
	e.mu.Lock()
	for id, te := range e.tunnels {
		if !te.pia || !te.link.forwards || e.tunnelUpLocked(id) == nil || te.pfBusy {
			continue
		}
		due := te.boundAt.IsZero() || now.Sub(te.boundAt) >= pfRebind
		if te.pfErr != "" {
			due = now.Sub(te.pfTried) >= pfRetry
		}
		if due {
			te.pfBusy = true
			jobs = append(jobs, job{id, te, te.t, te.on})
		}
	}
	p, api, regionID := e.pia, e.piaAPI, e.settings.PIARegion
	e.mu.Unlock()
	for _, j := range jobs {
		err := e.forwardPort(now, p, api, regionID, j.te, j.t, j.b)
		e.mu.Lock()
		j.te.pfBusy, j.te.pfTried = false, now
		if j.te.t == j.t { // still the same tunnel
			if err != nil {
				log.Printf("PIA port forwarding on %s: %v", j.id, err)
				j.te.pfErr = err.Error()
			} else {
				j.te.pfErr, j.te.boundAt = "", now
			}
		}
		e.mu.Unlock()
	}
}

func (e *Engine) forwardPort(now time.Time, p *piaState, api pia.API, regionID string, te *tunnelEntry, t *tunnel.Tunnel, b *netif.Binding) error {
	if p == nil {
		return errors.New("PIA isn't set up")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e.mu.Lock()
	sig, gws := te.sig, te.link.gateways
	e.mu.Unlock()
	var lastErr error
	for _, gw := range gws {
		if sig == nil || now.After(sig.Expires.Add(-24*time.Hour)) {
			token, _, err := e.piaSession(ctx, p, api, regionID, b)
			if err != nil {
				return err
			}
			s, err := api.Signature(ctx, t.DialContext, gw, token)
			if err != nil {
				lastErr = err
				continue
			}
			sig = &s
		}
		if err := api.BindPort(ctx, t.DialContext, gw, *sig); err != nil {
			lastErr = err
			continue
		}
		e.mu.Lock()
		if te.t == t {
			te.sig, te.pfPort = sig, sig.Port
			te.link.gateways = []pia.Server{gw} // the one that answers
		}
		e.mu.Unlock()
		return nil
	}
	return lastErr
}

// piaSession is a valid token and the region to use, logging in and reading
// the server list when needed.
func (e *Engine) piaSession(ctx context.Context, p *piaState, api pia.API, regionID string, b *netif.Binding) (string, pia.Region, error) {
	e.mu.Lock()
	token, tokenAt := p.token, p.tokenAt
	regions, regionsAt := p.regions, p.regionsAt
	login, picked := p.login, p.region.ID
	e.mu.Unlock()
	now := time.Now()
	if token == "" || now.Sub(tokenAt) > piaTokenLife {
		t, err := api.Token(ctx, b.HTTPClient(), login.User, login.Pass)
		e.mu.Lock()
		if errors.Is(err, pia.ErrLogin) && e.pia == p {
			p.refused, p.err = true, "PIA refused the username or password. Enter them again in settings."
		}
		e.mu.Unlock()
		if err != nil {
			return "", pia.Region{}, err
		}
		token, tokenAt = t, now
	}
	if regions == nil || now.Sub(regionsAt) > piaRegionsLife {
		r, err := api.Regions(ctx, b.HTTPClient())
		if err != nil {
			return "", pia.Region{}, err
		}
		regions, regionsAt = r, now
	}
	var region pia.Region
	for _, r := range regions {
		if r.ID == regionID {
			region = r
		}
	}
	if region.ID == "" && regionID == "" {
		// Every interface uses the region the first one measured fastest.
		for _, r := range regions {
			if r.ID == picked {
				region = r
			}
		}
	}
	if region.ID == "" { // "" or gone: the lowest latency
		r, err := api.Fastest(ctx, regions, b.DialContext)
		if err != nil {
			return "", pia.Region{}, err
		}
		region = r
	}
	e.mu.Lock()
	p.token, p.tokenAt, p.regions, p.regionsAt, p.region, p.err = token, tokenAt, regions, regionsAt, region, ""
	e.mu.Unlock()
	return token, region, nil
}

// piaRegions lists the regions for the settings page, through the main interface.
func (e *Engine) piaRegions() ([]map[string]any, error) {
	e.mu.Lock()
	ent := e.ifaces[paths.MainInterface(e.pathSnapshotLocked())]
	api := e.piaAPI
	e.mu.Unlock()
	if ent == nil {
		return nil, errors.New("no connection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	regions, err := api.Regions(ctx, ent.binding.HTTPClient())
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, r := range regions {
		out = append(out, map[string]any{"id": r.ID, "name": r.Name, "portForward": r.PortForward})
	}
	return out, nil
}

// restartPIATunnels makes the PIA tunnels connect again, as after a region change.
func (e *Engine) restartPIATunnels() {
	var stops []*tunnel.Tunnel
	e.mu.Lock()
	if e.pia != nil {
		e.pia.region = pia.Region{}
	}
	for _, te := range e.tunnels {
		if te.pia && te.t != nil {
			stops = append(stops, te.t)
			te.t, te.on, te.client, te.up, te.retryAt = nil, nil, nil, false, time.Time{}
		}
	}
	e.mu.Unlock()
	for _, t := range stops {
		t.Close()
	}
}

func (e *Engine) piaViewLocked() PIAView {
	p := e.pia
	if p == nil {
		return PIAView{State: "off"}
	}
	v := PIAView{Configured: true, State: "connecting", Error: p.err, Region: p.region.Name}
	switch {
	case p.refused:
		v.State = "login-failed"
	case p.err != "":
		v.State = "error"
	default:
		for id, te := range e.tunnels {
			if te.pia && e.tunnelUpLocked(id) != nil {
				v.State = "ok"
			}
		}
	}
	return v
}
