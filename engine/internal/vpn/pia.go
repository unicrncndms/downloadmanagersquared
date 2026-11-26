package vpn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"downloadmanagersquared/engine/internal/atomicfile"
	"downloadmanagersquared/engine/internal/netif"
	"downloadmanagersquared/engine/internal/pia"
	"downloadmanagersquared/engine/internal/socks"
	"downloadmanagersquared/engine/internal/tunnel"
	"downloadmanagersquared/engine/internal/winutil"
)

const (
	piaTokenLife   = 20 * time.Hour // PIA's tokens last 24
	piaRegionsLife = 6 * time.Hour
)

// piaProvider is PIA, the provider of every interface's tunnel that has no
// imported config, once the user signs in. It makes a new config (and key) at
// each start, and forwards a port through its API.
type piaProvider struct {
	t       *Tunnels
	api     pia.API
	region  string            // the one the user picked, "" = the lowest latency
	acct    *account          // nil: not signed in
	servers map[*entry]string // the server each tunnel holds, kept apart from the others'
	logins  sync.Mutex        // one login at a time, so starts that come together share its token
}

// account is a PIA login and what it has learned.
type account struct {
	login     piaLogin
	refused   bool // the login was refused: wait for the user to save it again
	err       string
	token     string
	tokenAt   time.Time
	regions   []pia.Region
	regionsAt time.Time
	region    pia.Region               // the one in use
	latency   map[string]time.Duration // each region's, for spreading tunnels (AutoRegion)
	latencyAt time.Time
}

// AutoRegion is an interface's PIA region when its tunnel takes the nearest
// one no other tunnel is in (pia.Spread), so each gets its own exit IP.
const AutoRegion = "auto"

// piaLogin is what pia.dat holds, encrypted with DPAPI.
type piaLogin struct {
	User string `json:"user"`
	Pass string `json:"pass"`
}

// PIAView is PIA as the settings page shows it; never the login itself.
type PIAView struct {
	Configured bool   `json:"configured"`
	State      string `json:"state"` // "off", "connecting", "ok", "login-failed", "error"
	Error      string `json:"error,omitempty"`
	Region     string `json:"region,omitempty"`
}

func (*piaProvider) name() string         { return "pia" }
func (*piaProvider) typedPort(*entry) int { return 0 }  // PIA forwards its own
func (*piaProvider) lostHint() string     { return "" } // a new key each start: nothing to expire

// ready: with no login, or one PIA refused, PIA waits for the user. PIA's
// servers have IPv4 endpoints only, so an interface without IPv4 gets none.
func (p *piaProvider) ready(te *entry) bool {
	if te == nil { // whether to add tunnels at all
		return p.acct != nil && !p.acct.refused
	}
	if b := p.t.ifaces[te.id].Binding; b != nil && !b.Has4() {
		te.err = errPIANeedsIPv4
		return false
	}
	return p.acct != nil && !p.acct.refused
}

const errPIANeedsIPv4 = "PIA needs IPv4"

func (p *piaProvider) file() string { return filepath.Join(p.t.dir, "pia.dat") }

// load reads the saved login. One that can't be read is set aside.
func (p *piaProvider) load() error {
	b, err := os.ReadFile(p.file())
	if errors.Is(err, os.ErrNotExist) {
		return nil
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
		setAside(p.file())
		return err
	}
	p.t.mu.Lock()
	p.acct = &account{login: l}
	p.t.mu.Unlock()
	return nil
}

// addWanted gives a PIA tunnel to each usable interface without a tunnel of
// its own, while signed in.
func (p *piaProvider) addWanted() {
	t := p.t
	t.mu.Lock()
	var ids []string
	if p.ready(nil) {
		for id, i := range t.ifaces {
			if i.Usable && i.Binding != nil && t.tunnels[id] == nil {
				ids = append(ids, id)
			}
		}
	}
	t.mu.Unlock()
	for _, id := range ids {
		srv, err := socks.Listen(t.socksPath(id))
		if err != nil {
			continue
		}
		t.mu.Lock()
		if t.tunnels[id] == nil && t.ctx.Err() == nil { // not after Close
			t.tunnels[id] = &entry{id: id, prov: p, socks: srv}
			srv = nil
		}
		t.mu.Unlock()
		if srv != nil {
			srv.Close()
		}
	}
}

// ours are the interfaces with a PIA tunnel.
func (p *piaProvider) oursLocked() []*entry {
	var out []*entry
	for _, te := range p.t.tunnels {
		if te.prov == provider(p) {
			out = append(out, te)
		}
	}
	return out
}

// SetPIA saves a new login (or with user "" forgets PIA), and gives every
// interface without a .conf of its own a PIA tunnel.
func (t *Tunnels) SetPIA(user, pass string) error {
	p := t.pia
	if user == "" {
		t.mu.Lock()
		p.acct = nil
		var ids []string
		for _, te := range p.oursLocked() {
			ids = append(ids, te.id)
		}
		p.servers = map[*entry]string{}
		t.mu.Unlock()
		for _, id := range ids {
			t.removeEntry(id)
		}
		if err := os.Remove(p.file()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	plain, _ := json.Marshal(piaLogin{user, pass})
	sealed, err := winutil.Protect(plain)
	if err != nil {
		return err
	}
	if err := atomicfile.Write(p.file(), sealed, 0o600); err != nil {
		return err
	}
	t.mu.Lock()
	p.acct = &account{login: piaLogin{user, pass}}
	for _, te := range p.oursLocked() {
		te.retryAt = time.Time{} // try the new login at once
	}
	t.mu.Unlock()
	t.sync(time.Now())
	return nil
}

// SetPIARegion picks PIA's region ("" = the lowest latency) and makes the PIA
// tunnels connect again.
func (t *Tunnels) SetPIARegion(region string) {
	p := t.pia
	var stops []*tunnel.Tunnel
	t.mu.Lock()
	p.region = region
	if p.acct != nil {
		p.acct.region = pia.Region{}
	}
	for _, te := range p.oursLocked() {
		if t.ifaces[te.id].PIARegion != "" {
			continue // a region of its own: this setting isn't its
		}
		if tn := te.stopLocked(); tn != nil {
			stops = append(stops, tn)
			te.retryAt = time.Time{}
		}
	}
	t.mu.Unlock()
	for _, tn := range stops {
		tn.Close()
	}
}

// config registers a new key for one interface, through that interface.
func (p *piaProvider) config(ctx context.Context, te *entry, b *netif.Binding) (tunnel.Config, func(*tunnel.Tunnel) forwarder, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	p.t.mu.Lock()
	a, regionID := p.acct, p.regionForLocked(te.id)
	te.region = regionID
	p.t.mu.Unlock()
	if a == nil {
		return tunnel.Config{}, nil, errors.New("PIA isn't set up")
	}
	token, region, err := p.session(ctx, a, regionID, b, te)
	if err != nil {
		return tunnel.Config{}, nil, err
	}
	p.t.mu.Lock()
	te.regionName = region.Name
	p.t.mu.Unlock()
	fwRegion := regionID
	if regionID == AutoRegion { // port forwarding asks in the region it was given, not for another spread
		fwRegion = region.ID
	}
	priv, pub, err := tunnel.GenerateKey()
	if err != nil {
		return tunnel.Config{}, nil, err
	}
	srv, err := p.reserveServer(te, region)
	if err != nil {
		return tunnel.Config{}, nil, err
	}
	k, err := p.api.AddKey(ctx, b.DialContext, srv, token, pub)
	if err != nil {
		p.t.mu.Lock()
		a.token = "" // maybe it expired early: log in again next time
		p.t.mu.Unlock()
		return tunnel.Config{}, nil, err
	}
	cfg, err := tunnel.ParseConf(k.Conf(priv, srv))
	if err != nil {
		return tunnel.Config{}, nil, err
	}
	// PIA's README puts port forwarding on the server's address inside the
	// tunnel; its scripts use the server's own. Try both.
	var gws []pia.Server
	if k.ServerVIP != "" {
		gws = append(gws, pia.Server{IP: k.ServerVIP, CN: srv.CN})
	}
	gws = append(gws, srv)
	return cfg, func(tn *tunnel.Tunnel) forwarder {
		return &piaForwarder{p: p, id: te.id, tn: tn, b: b, region: fwRegion, forwards: region.PortForward, gateways: gws}
	}, nil
}

// regionForLocked is the PIA region an interface's tunnel uses: its own, else
// PIA's setting ("" = the lowest latency).
func (p *piaProvider) regionForLocked(ifaceID string) string {
	if r := p.t.ifaces[ifaceID].PIARegion; r != "" {
		return r
	}
	return p.region
}

// reserveServer picks a server of region for te that no other running tunnel
// holds. PIA gives an account one peer IP per server and routes it to the
// newest key, so a second tunnel on a server takes it from the first, which
// then reconnects and takes it back: neither keeps its peers. With every
// server held, te waits instead, retrying, until one is free or the user picks
// another region for its interface. Apart, each tunnel gets its own exit IP.
func (p *piaProvider) reserveServer(te *entry, region pia.Region) (pia.Server, error) {
	servers := region.WG()
	if len(servers) == 0 {
		return pia.Server{}, errors.New("PIA's region has no WireGuard server")
	}
	p.t.mu.Lock()
	defer p.t.mu.Unlock()
	held := map[string]bool{}
	for _, o := range p.oursLocked() {
		if s := p.servers[o]; o != te && s != "" && (o.t != nil || o.starting) {
			held[s] = true
		}
	}
	for _, s := range servers {
		if !held[s.CN] {
			p.servers[te] = s.CN
			return s, nil
		}
	}
	delete(p.servers, te)
	taken := "PIA's only server in " + region.Name + " carries another connection's tunnel"
	if len(servers) > 1 {
		taken = fmt.Sprintf("PIA's %d servers in %s carry other connections' tunnels", len(servers), region.Name)
	}
	return pia.Server{}, errors.New(taken + ", and a server carries one tunnel at a time. Pick another region for this connection")
}

const (
	pfRebind = 15 * time.Minute // PIA drops a port not bound again within this
	pfRetry  = time.Minute
	pfShown  = 20 * time.Minute // a bind older than this no longer counts as reachable
)

// piaForwarder is PIA's port forwarding for one start of a tunnel.
type piaForwarder struct {
	p        *piaProvider
	id       string
	tn       *tunnel.Tunnel
	b        *netif.Binding
	region   string       // the region its tunnel asked for
	forwards bool         // the region forwards ports
	gateways []pia.Server // where port forwarding answers inside the tunnel, best first
	sig      *pia.Signature
	fport    int // forwarded to the tunnel, 0 if none
	boundAt  time.Time
	tried    time.Time
	err      string
	busy     bool
}

// forward gets a port in a forwarding region, and binds it again every 15 minutes.
func (f *piaForwarder) forward(now time.Time) {
	if !f.forwards || f.busy {
		return
	}
	due := f.boundAt.IsZero() || now.Sub(f.boundAt) >= pfRebind
	if f.err != "" {
		due = now.Sub(f.tried) >= pfRetry
	}
	if !due {
		return
	}
	f.busy = true
	a, regionID := f.p.acct, f.region
	f.p.t.goJob(func() {
		err := f.bind(now, a, regionID)
		f.p.t.mu.Lock()
		defer f.p.t.mu.Unlock()
		f.busy, f.tried = false, now
		if err != nil {
			log.Printf("PIA port forwarding on %s: %v", f.id, err)
			f.err = err.Error()
		} else {
			f.err, f.boundAt = "", now
		}
	})
}

func (f *piaForwarder) port(now time.Time) (int, string) {
	switch {
	case !f.forwards:
		return f.fport, "outgoing"
	case f.fport != 0 && now.Sub(f.boundAt) < pfShown:
		return f.fport, "reachable"
	case f.err != "":
		return f.fport, "outgoing"
	}
	return f.fport, ""
}

// bind gets a signature if it has none (or it is about to expire), and binds its port.
func (f *piaForwarder) bind(now time.Time, a *account, regionID string) error {
	if a == nil {
		return errors.New("PIA isn't set up")
	}
	ctx, cancel := context.WithTimeout(f.p.t.ctx, 30*time.Second)
	defer cancel()
	f.p.t.mu.Lock()
	sig, gws := f.sig, f.gateways
	f.p.t.mu.Unlock()
	var lastErr error
	for _, gw := range gws {
		if sig == nil || now.After(sig.Expires.Add(-24*time.Hour)) {
			token, _, err := f.p.session(ctx, a, regionID, f.b, nil)
			if err != nil {
				return err
			}
			s, err := f.p.api.Signature(ctx, f.tn.DialContext, gw, token)
			if err != nil {
				lastErr = err
				continue
			}
			sig = &s
		}
		if err := f.p.api.BindPort(ctx, f.tn.DialContext, gw, *sig); err != nil {
			lastErr = err
			continue
		}
		f.p.t.mu.Lock()
		f.sig, f.fport = sig, sig.Port
		f.gateways = []pia.Server{gw} // the one that answers
		f.p.t.mu.Unlock()
		return nil
	}
	return lastErr
}

// login gets a token for a's login, unless a concurrent start has just got one:
// the token is kept on the account before the lock is released.
func (p *piaProvider) login(ctx context.Context, a *account, b *netif.Binding, login piaLogin) (string, time.Time, error) {
	p.logins.Lock()
	defer p.logins.Unlock()
	t := p.t
	t.mu.Lock()
	token, tokenAt := a.token, a.tokenAt
	t.mu.Unlock()
	if token != "" && time.Since(tokenAt) <= piaTokenLife {
		return token, tokenAt, nil
	}
	tok, err := p.api.Token(ctx, b.HTTPClient(), login.User, login.Pass)
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if errors.Is(err, pia.ErrLogin) && p.acct == a {
		a.refused, a.err = true, "PIA refused the username or password. Enter them again in settings."
	}
	if err != nil {
		return "", time.Time{}, err
	}
	if p.acct == a {
		a.token, a.tokenAt = tok, now
	}
	return tok, now, nil
}

// session is a valid token and the region to use, logging in and reading
// the server list when needed. te is the tunnel it is for, which AutoRegion
// keeps apart from the others (nil: none, for a token only).
func (p *piaProvider) session(ctx context.Context, a *account, regionID string, b *netif.Binding, te *entry) (string, pia.Region, error) {
	t := p.t
	t.mu.Lock()
	token, tokenAt := a.token, a.tokenAt
	regions, regionsAt := a.regions, a.regionsAt
	login, picked := a.login, a.region.ID
	t.mu.Unlock()
	now := time.Now()
	if token == "" || now.Sub(tokenAt) > piaTokenLife {
		var err error
		if token, tokenAt, err = p.login(ctx, a, b, login); err != nil {
			return "", pia.Region{}, err
		}
	}
	if regions == nil || now.Sub(regionsAt) > piaRegionsLife {
		r, err := p.api.Regions(ctx, b.HTTPClient())
		if err != nil {
			return "", pia.Region{}, err
		}
		regions, regionsAt = r, now
	}
	find := func(id string) (region pia.Region) {
		for _, r := range regions {
			if r.ID == id {
				region = r
			}
		}
		return
	}
	region := find(regionID)
	if regionID == AutoRegion {
		r, err := p.spread(ctx, a, regions, b, te)
		if err != nil {
			return "", pia.Region{}, err
		}
		region = r
	}
	if region.ID == "" && regionID == "" {
		// Every interface uses the region the first one measured fastest.
		region = find(picked)
	}
	if region.ID == "" { // "" or gone: the lowest latency
		// Under the logins lock, so interfaces starting together probe once:
		// the later ones find the first one's pick stored.
		p.logins.Lock()
		t.mu.Lock()
		picked = a.region.ID
		t.mu.Unlock()
		if region = find(picked); region.ID == "" {
			r, err := p.api.Fastest(ctx, regions, b.DialContext)
			if err != nil {
				p.logins.Unlock()
				return "", pia.Region{}, err
			}
			region = r
		}
		t.mu.Lock()
		a.region = region
		t.mu.Unlock()
		p.logins.Unlock()
	}
	t.mu.Lock()
	a.token, a.tokenAt, a.regions, a.regionsAt, a.err = token, tokenAt, regions, regionsAt, ""
	if regionID == p.region { // an interface's own region isn't the one every other uses
		a.region = region
	}
	t.mu.Unlock()
	return token, region, nil
}

// spread is the region for te under AutoRegion: the nearest (port forwarding
// first) that no other running PIA tunnel is in. Latencies are measured once
// per server list, under the logins lock so tunnels starting together probe once.
func (p *piaProvider) spread(ctx context.Context, a *account, regions []pia.Region, b *netif.Binding, te *entry) (pia.Region, error) {
	p.logins.Lock()
	defer p.logins.Unlock()
	p.t.mu.Lock()
	lat, at := a.latency, a.latencyAt
	p.t.mu.Unlock()
	if lat == nil || time.Since(at) > piaRegionsLife {
		lat = p.api.Latencies(ctx, regions, b.DialContext)
		p.t.mu.Lock()
		a.latency, a.latencyAt = lat, time.Now()
		p.t.mu.Unlock()
	}
	p.t.mu.Lock()
	defer p.t.mu.Unlock()
	taken := map[string]bool{}
	for _, o := range p.oursLocked() {
		if o != te && o.got != "" && (o.t != nil || o.starting) {
			taken[o.got] = true
		}
	}
	r, ok := pia.Spread(regions, lat, taken)
	if !ok {
		return pia.Region{}, errors.New("no PIA region answered")
	}
	if te != nil {
		te.got = r.ID // held while it starts, so the next tunnel takes another
	}
	return r, nil
}

// PIARegions lists PIA's regions for the settings page, through b, with how
// quickly each answers ("ms", left out where it didn't) and where it is.
func (t *Tunnels) PIARegions(b *netif.Binding) ([]map[string]any, error) {
	ctx, cancel := context.WithTimeout(t.ctx, 20*time.Second)
	defer cancel()
	regions, err := t.pia.api.Regions(ctx, b.HTTPClient())
	if err != nil {
		return nil, err
	}
	lat := t.pia.api.Latencies(ctx, regions, b.DialContext)
	out := []map[string]any{}
	for _, r := range regions {
		m := map[string]any{"id": r.ID, "name": r.Name, "portForward": r.PortForward, "country": r.Country}
		if ms, ok := lat[r.ID]; ok {
			m["ms"] = ms.Milliseconds()
		}
		out = append(out, m)
	}
	return out, nil
}

// PIA is PIA's account as the settings page shows it.
func (t *Tunnels) PIA() PIAView {
	t.mu.Lock()
	defer t.mu.Unlock()
	a := t.pia.acct
	if a == nil {
		return PIAView{State: "off"}
	}
	v := PIAView{Configured: true, State: "connecting", Error: a.err, Region: a.region.Name}
	switch {
	case a.refused:
		v.State = "login-failed"
	case a.err != "":
		v.State = "error"
	default:
		for _, te := range t.pia.oursLocked() {
			if t.upLocked(te.id) != nil {
				v.State = "ok"
			}
		}
	}
	return v
}
