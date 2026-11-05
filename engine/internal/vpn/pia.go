package vpn

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"downloadmanagersquared/engine/internal/atomicfile"
	"downloadmanagersquared/engine/internal/netif"
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
// .conf of its own. nil in Tunnels.pia means it isn't set up.
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

func (t *Tunnels) piaFile() string { return filepath.Join(t.dir, "pia.dat") }

// loadPIA reads the saved login. One that can't be read is set aside.
func (t *Tunnels) loadPIA() error {
	b, err := os.ReadFile(t.piaFile())
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
		setAside(t.piaFile())
		return err
	}
	t.mu.Lock()
	t.pia = &piaState{login: l}
	t.mu.Unlock()
	return nil
}

// SetPIA saves a new login (or with user "" forgets PIA), and gives every
// interface without a .conf of its own a PIA tunnel.
func (t *Tunnels) SetPIA(user, pass string) error {
	if user == "" {
		t.mu.Lock()
		t.pia = nil
		var ids []string
		for id, te := range t.tunnels {
			if te.pia {
				ids = append(ids, id)
			}
		}
		t.mu.Unlock()
		for _, id := range ids {
			t.removeEntry(id)
		}
		if err := os.Remove(t.piaFile()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	plain, _ := json.Marshal(piaLogin{user, pass})
	sealed, err := winutil.Protect(plain)
	if err != nil {
		return err
	}
	if err := atomicfile.Write(t.piaFile(), sealed, 0o600); err != nil {
		return err
	}
	t.mu.Lock()
	t.pia = &piaState{login: piaLogin{user, pass}}
	for _, te := range t.tunnels {
		if te.pia {
			te.retryAt = time.Time{} // try the new login at once
		}
	}
	t.mu.Unlock()
	t.sync(time.Now())
	return nil
}

// SetPIARegion picks PIA's region ("" = the lowest latency) and makes the PIA
// tunnels connect again.
func (t *Tunnels) SetPIARegion(region string) {
	var stops []*tunnel.Tunnel
	t.mu.Lock()
	t.region = region
	if t.pia != nil {
		t.pia.region = pia.Region{}
	}
	for _, te := range t.tunnels {
		if te.pia && te.t != nil {
			stops = append(stops, te.t)
			te.t, te.on, te.client, te.up, te.retryAt = nil, nil, nil, false, time.Time{}
		}
	}
	t.mu.Unlock()
	for _, tn := range stops {
		tn.Close()
	}
}

// piaWantedLocked are the usable interfaces that should get a PIA tunnel:
// those without a tunnel of their own.
func (t *Tunnels) piaWantedLocked() []string {
	if t.pia == nil || t.pia.refused {
		return nil
	}
	var ids []string
	for id, i := range t.ifaces {
		if i.Usable && i.Binding != nil && t.tunnels[id] == nil {
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
func (t *Tunnels) piaConfig(te *entry, b *netif.Binding) (tunnel.Config, piaLink, error) {
	ctx, cancel := context.WithTimeout(t.ctx, 45*time.Second)
	defer cancel()
	t.mu.Lock()
	p, regionID := t.pia, t.region
	t.mu.Unlock()
	if p == nil {
		return tunnel.Config{}, piaLink{}, errors.New("PIA isn't set up")
	}
	token, region, err := t.piaSession(ctx, p, regionID, b)
	if err != nil {
		return tunnel.Config{}, piaLink{}, err
	}
	priv, pub, err := tunnel.GenerateKey()
	if err != nil {
		return tunnel.Config{}, piaLink{}, err
	}
	srv, err := t.reservePIAServer(te, region.WG())
	if err != nil {
		return tunnel.Config{}, piaLink{}, err
	}
	k, err := t.api.AddKey(ctx, b.DialContext, srv, token, pub)
	if err != nil {
		t.mu.Lock()
		p.token = "" // maybe it expired early: log in again next time
		t.mu.Unlock()
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
func (t *Tunnels) reservePIAServer(te *entry, servers []pia.Server) (pia.Server, error) {
	if len(servers) == 0 {
		return pia.Server{}, errors.New("PIA's region has no WireGuard server")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	held := map[string]int{}
	for _, o := range t.tunnels {
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

// forwardPIA gets each PIA tunnel in a forwarding region a port, and binds it
// again every 15 minutes. Each tunnel's binding runs on its own.
func (t *Tunnels) forwardPIA(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, regionID := t.pia, t.region
	for id, te := range t.tunnels {
		if !te.pia || !te.link.forwards || t.upLocked(id) == nil || te.pfBusy {
			continue
		}
		due := te.boundAt.IsZero() || now.Sub(te.boundAt) >= pfRebind
		if te.pfErr != "" {
			due = now.Sub(te.pfTried) >= pfRetry
		}
		if !due {
			continue
		}
		te.pfBusy = true
		tn, b := te.t, te.on
		t.goJob(func() {
			err := t.forwardPort(now, p, regionID, te, tn, b)
			t.mu.Lock()
			defer t.mu.Unlock()
			te.pfBusy, te.pfTried = false, now
			if te.t != tn { // no longer the same tunnel
				return
			}
			if err != nil {
				log.Printf("PIA port forwarding on %s: %v", id, err)
				te.pfErr = err.Error()
			} else {
				te.pfErr, te.boundAt = "", now
			}
		})
	}
}

func (t *Tunnels) forwardPort(now time.Time, p *piaState, regionID string, te *entry, tn *tunnel.Tunnel, b *netif.Binding) error {
	if p == nil {
		return errors.New("PIA isn't set up")
	}
	ctx, cancel := context.WithTimeout(t.ctx, 30*time.Second)
	defer cancel()
	t.mu.Lock()
	sig, gws := te.sig, te.link.gateways
	t.mu.Unlock()
	var lastErr error
	for _, gw := range gws {
		if sig == nil || now.After(sig.Expires.Add(-24*time.Hour)) {
			token, _, err := t.piaSession(ctx, p, regionID, b)
			if err != nil {
				return err
			}
			s, err := t.api.Signature(ctx, tn.DialContext, gw, token)
			if err != nil {
				lastErr = err
				continue
			}
			sig = &s
		}
		if err := t.api.BindPort(ctx, tn.DialContext, gw, *sig); err != nil {
			lastErr = err
			continue
		}
		t.mu.Lock()
		if te.t == tn {
			te.sig, te.pfPort = sig, sig.Port
			te.link.gateways = []pia.Server{gw} // the one that answers
		}
		t.mu.Unlock()
		return nil
	}
	return lastErr
}

// piaSession is a valid token and the region to use, logging in and reading
// the server list when needed.
func (t *Tunnels) piaSession(ctx context.Context, p *piaState, regionID string, b *netif.Binding) (string, pia.Region, error) {
	t.mu.Lock()
	token, tokenAt := p.token, p.tokenAt
	regions, regionsAt := p.regions, p.regionsAt
	login, picked := p.login, p.region.ID
	t.mu.Unlock()
	now := time.Now()
	if token == "" || now.Sub(tokenAt) > piaTokenLife {
		tok, err := t.api.Token(ctx, b.HTTPClient(), login.User, login.Pass)
		t.mu.Lock()
		if errors.Is(err, pia.ErrLogin) && t.pia == p {
			p.refused, p.err = true, "PIA refused the username or password. Enter them again in settings."
		}
		t.mu.Unlock()
		if err != nil {
			return "", pia.Region{}, err
		}
		token, tokenAt = tok, now
	}
	if regions == nil || now.Sub(regionsAt) > piaRegionsLife {
		r, err := t.api.Regions(ctx, b.HTTPClient())
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
		r, err := t.api.Fastest(ctx, regions, b.DialContext)
		if err != nil {
			return "", pia.Region{}, err
		}
		region = r
	}
	t.mu.Lock()
	p.token, p.tokenAt, p.regions, p.regionsAt, p.region, p.err = token, tokenAt, regions, regionsAt, region, ""
	t.mu.Unlock()
	return token, region, nil
}

// PIARegions lists PIA's regions for the settings page, through client.
func (t *Tunnels) PIARegions(client *http.Client) ([]map[string]any, error) {
	ctx, cancel := context.WithTimeout(t.ctx, 20*time.Second)
	defer cancel()
	regions, err := t.api.Regions(ctx, client)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, r := range regions {
		out = append(out, map[string]any{"id": r.ID, "name": r.Name, "portForward": r.PortForward})
	}
	return out, nil
}

// PIA is PIA's account as the settings page shows it.
func (t *Tunnels) PIA() PIAView {
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.pia
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
		for id, te := range t.tunnels {
			if te.pia && t.upLocked(id) != nil {
				v.State = "ok"
			}
		}
	}
	return v
}
