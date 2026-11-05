// Package vpn runs the VPN tunnels: one WireGuard tunnel per interface, from
// its provider's config (an imported .conf, or PIA), with its forwarded port
// and the SOCKS proxy that resolves landing pages through it.
//
// The engine drives it with Tick once a second, passing the interfaces as they
// are, and reads back what is up (Up) and what to show (Status, PIA). It never
// calls back into the engine.
package vpn

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"downloadmanagersquared/engine/internal/atomicfile"
	"downloadmanagersquared/engine/internal/netif"
	"downloadmanagersquared/engine/internal/pia"
	"downloadmanagersquared/engine/internal/socks"
	"downloadmanagersquared/engine/internal/tunnel"
)

const (
	tunnelRetry = 30 * time.Second // after a tunnel fails to start or loses its server
	tunnelGrace = 30 * time.Second // for a new tunnel's first handshake
)

var errNotUp = errors.New("the VPN tunnel is not up")

// Iface is an interface as the engine sees it at a Tick.
type Iface struct {
	ID      string
	Name    string         // for messages; "" if not known
	Binding *netif.Binding // nil if it isn't connected
	Usable  bool           // present and enabled: its tunnel may run
	// ForwardedPort is a port the user typed in for its imported tunnel, 0 if none.
	ForwardedPort int
}

// Options set up Tunnels.
type Options struct {
	Dir       string                // the engine's folder: tunnels\ and pia.dat
	PIA       pia.API               // zero: pia.Default
	PIARegion string                // the region the user picked, "" = the lowest latency
	GatewayOK func(netip.Addr) bool // where NAT-PMP may be sent; nil: private and CGNAT addresses
}

// Problem is a saved config or login that couldn't be read at Load and was set aside.
type Problem struct {
	Iface string // the interface whose .conf it was; "" for the PIA login
	Err   error
}

// Tunnels are the VPN tunnels of every interface.
type Tunnels struct {
	dir       string
	api       pia.API
	gatewayOK func(netip.Addr) bool
	ctx       context.Context // cancelled by Close; every job takes it
	cancel    context.CancelFunc
	jobs      sync.WaitGroup

	mu      sync.Mutex
	tunnels map[string]*entry // by interface ID
	ifaces  map[string]Iface  // as of the last Tick
	pia     *piaState         // nil: PIA isn't set up
	region  string
}

// entry is the tunnel configured for one interface.
type entry struct {
	pia      bool          // PIA makes a new config (and key) at each start
	server   string        // PIA: the server's name, kept apart from other tunnels'
	cfg      tunnel.Config // a .conf's, or PIA's latest
	socks    *socks.Server // landing pages resolved through the tunnel
	t        *tunnel.Tunnel
	on       *netif.Binding // what t runs over
	client   *http.Client
	up       bool
	gen      int // counts starts: torrent sockets of an older one are on a dead stack
	starting bool
	started  time.Time
	retryAt  time.Time
	err      string

	// PIA port forwarding, for this tunnel's server.
	link    piaLink
	sig     *pia.Signature
	pfPort  int // forwarded to the tunnel, 0 if none
	boundAt time.Time
	pfTried time.Time
	pfErr   string
	pfBusy  bool

	// NAT-PMP port forwarding, for an imported tunnel (pfPort holds its port).
	pmp pmpState
}

func New(o Options) *Tunnels {
	if o.PIA.TokenURL == "" {
		o.PIA = pia.Default
	}
	if o.GatewayOK == nil {
		o.GatewayOK = privateGateway
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Tunnels{
		dir: o.Dir, api: o.PIA, gatewayOK: o.GatewayOK, ctx: ctx, cancel: cancel,
		tunnels: map[string]*entry{}, ifaces: map[string]Iface{}, region: o.PIARegion,
	}
}

// Load reads the saved configs and the PIA login. What can't be read is set
// aside (renamed .broken) and returned, for the engine to tell the user.
func (t *Tunnels) Load() []Problem {
	var problems []Problem
	files, _ := filepath.Glob(filepath.Join(t.dir, "tunnels", "*.conf"))
	for _, f := range files {
		id, err := hex.DecodeString(strings.TrimSuffix(filepath.Base(f), ".conf"))
		if err != nil {
			continue
		}
		b, err := os.ReadFile(f)
		if err == nil {
			_, err = tunnel.ParseConf(string(b))
		}
		if err != nil && !errors.Is(err, tunnel.ErrNoDNS) { // that one shows on the tunnel
			log.Printf("tunnel for %s unreadable, set aside: %v", id, err)
			setAside(f)
			problems = append(problems, Problem{Iface: string(id), Err: err})
			continue
		}
		if err := t.setTunnel(string(id), string(b), false); err != nil {
			log.Printf("tunnel for %s: %v", id, err)
		}
	}
	if err := t.loadPIA(); err != nil {
		problems = append(problems, Problem{Err: err})
	}
	return problems
}

func setAside(path string) {
	if err := os.Rename(path, path+".broken"); err != nil {
		log.Printf("setting aside %s: %v", path, err)
	}
}

// Tick follows the interfaces as they are now: it starts, stops and restarts
// tunnels, and keeps their ports forwarded. Slow work runs in the background.
func (t *Tunnels) Tick(now time.Time, ifaces []Iface) {
	t.mu.Lock()
	t.ifaces = make(map[string]Iface, len(ifaces))
	for _, i := range ifaces {
		t.ifaces[i.ID] = i
	}
	t.mu.Unlock()
	t.sync(now)
	t.forwardPIA(now)
	t.forwardPMP(now)
}

// Wait waits for the background work started so far: tunnel starts and port
// forwarding. For tests: call it between Ticks, never alongside one.
func (t *Tunnels) Wait() { t.jobs.Wait() }

// Close takes every tunnel and proxy down. Work still running finds its tunnel
// gone and drops its result.
func (t *Tunnels) Close() {
	t.cancel()
	t.mu.Lock()
	ids := make([]string, 0, len(t.tunnels))
	for id := range t.tunnels {
		ids = append(ids, id)
	}
	t.mu.Unlock()
	for _, id := range ids {
		t.removeEntry(id)
	}
}

// go runs f as background work, cancelled by Close.
func (t *Tunnels) goJob(f func()) {
	t.jobs.Add(1)
	go func() {
		defer t.jobs.Done()
		f()
	}()
}

// The .conf files hold private keys, so they live in the engine's folder and
// never in the settings the extension gets.
func (t *Tunnels) tunnelFile(ifaceID string) string {
	return filepath.Join(t.dir, "tunnels", hex.EncodeToString([]byte(ifaceID))+".conf")
}

// Import makes conf the tunnel of an interface, replacing any it had, and
// starts it. A config is refused if it isn't a usable WireGuard one, or if
// another interface's imported tunnel has its key.
func (t *Tunnels) Import(ifaceID, conf string) error {
	if other := t.tunnelWithKey(ifaceID, conf); other != "" {
		return fmt.Errorf("%s already uses this config. Each connection needs its own: the VPN server "+
			"keeps one location per key, so two tunnels with one key take each other's traffic. "+
			"Have your VPN provider make another config (a separate device or key), ideally on another server", other)
	}
	if err := t.setTunnel(ifaceID, conf, true); err != nil {
		return fmt.Errorf("not a usable WireGuard config: %w", err)
	}
	t.sync(time.Now())
	return nil
}

// setTunnel makes conf the tunnel of an interface, replacing any it had. A
// saved config without DNS (from before that was required) is kept, its tunnel
// down saying why.
func (t *Tunnels) setTunnel(ifaceID, conf string, save bool) error {
	cfg, err := tunnel.ParseConf(conf)
	var cantRun string
	if errors.Is(err, tunnel.ErrNoDNS) && !save {
		cantRun, err = err.Error(), nil
	}
	if err != nil {
		return err
	}
	if save {
		if err := atomicfile.Write(t.tunnelFile(ifaceID), []byte(conf), 0o600); err != nil {
			return err
		}
	}
	t.removeEntry(ifaceID)
	srv, err := socks.Listen(t.socksPath(ifaceID))
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.tunnels[ifaceID] = &entry{cfg: cfg, socks: srv, err: cantRun}
	t.mu.Unlock()
	return nil
}

// socksPath sends a resolve's connections through an interface's tunnel.
func (t *Tunnels) socksPath(ifaceID string) socks.Path {
	return socks.Path{
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			u, ok := t.Up(ifaceID)
			if !ok {
				return nil, errNotUp
			}
			return u.Dial(ctx, network, addr)
		},
		Lookup: func(ctx context.Context, host string) ([]net.IP, error) {
			u, ok := t.Up(ifaceID)
			if !ok {
				return nil, errNotUp
			}
			return u.LookupIP(ctx, host)
		},
	}
}

// tunnelWithKey is the name of another interface whose imported config has
// the same private key as conf ("" if none, or conf doesn't parse).
func (t *Tunnels) tunnelWithKey(ifaceID, conf string) string {
	cfg, err := tunnel.ParseConf(conf)
	if err != nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, te := range t.tunnels {
		if id == ifaceID || te.pia || te.cfg.PrivateKey != cfg.PrivateKey {
			continue
		}
		if i := t.ifaces[id]; i.Name != "" {
			return i.Name
		}
		return "Another connection"
	}
	return ""
}

// Remove takes an interface's imported tunnel down and deletes its config.
func (t *Tunnels) Remove(ifaceID string) error {
	t.removeEntry(ifaceID)
	if err := os.Remove(t.tunnelFile(ifaceID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (t *Tunnels) removeEntry(ifaceID string) {
	t.mu.Lock()
	te := t.tunnels[ifaceID]
	delete(t.tunnels, ifaceID)
	t.mu.Unlock()
	if te != nil {
		te.socks.Close()
		if te.t != nil {
			te.t.Close()
		}
	}
}

// sync runs a tunnel for each interface that has one and is usable, following
// the interface's address, and restarts a tunnel whose server stopped
// answering, like an unreachable interface retrying.
func (t *Tunnels) sync(now time.Time) {
	type start struct {
		id string
		te *entry
		b  *netif.Binding
	}
	t.mu.Lock()
	wanted := t.piaWantedLocked()
	t.mu.Unlock()
	for _, id := range wanted {
		srv, err := socks.Listen(t.socksPath(id))
		if err != nil {
			continue
		}
		t.mu.Lock()
		if t.tunnels[id] == nil {
			t.tunnels[id] = &entry{pia: true, socks: srv}
			srv = nil
		}
		t.mu.Unlock()
		if srv != nil {
			srv.Close()
		}
	}

	var starts []start
	var stops []*tunnel.Tunnel
	t.mu.Lock()
	for id, te := range t.tunnels {
		ifc := t.ifaces[id]
		usable := ifc.Usable && ifc.Binding != nil
		if te.t != nil {
			te.up = te.t.Up()
			lost := !te.up && now.Sub(te.started) > tunnelGrace
			if !usable || te.on != ifc.Binding || lost {
				if lost {
					te.err, te.retryAt = "the VPN server doesn't answer", now.Add(tunnelRetry)
					if !te.pia { // PIA gets a new key each start; a .conf's can expire or be revoked
						te.err += ". If this keeps happening, the config may have expired or been revoked at your provider: make a new one and import it again"
					}
				}
				stops = append(stops, te.t)
				te.t, te.on, te.client, te.up = nil, nil, nil, false
			}
		}
		waitsForUser := te.pia && (t.pia == nil || t.pia.refused)
		cantRun := !te.pia && len(te.cfg.DNS) == 0 // its error says why
		if usable && te.t == nil && !te.starting && !now.Before(te.retryAt) && !waitsForUser && !cantRun {
			te.starting = true
			starts = append(starts, start{id, te, ifc.Binding})
		}
	}
	t.mu.Unlock()
	for _, tn := range stops {
		tn.Close()
	}
	for _, s := range starts {
		t.goJob(func() { t.startTunnel(s.id, s.te, s.b) })
	}
}

func (t *Tunnels) startTunnel(id string, te *entry, b *netif.Binding) {
	t.mu.Lock()
	cfg := te.cfg
	t.mu.Unlock()
	var err error
	var link piaLink
	if te.pia {
		// A new key each start: one PIA rejected or forgot is replaced this way.
		cfg, link, err = t.piaConfig(te, b)
	}
	var tn *tunnel.Tunnel
	if err == nil {
		tn, err = tunnel.Start(cfg, func() (net.PacketConn, error) { return b.ListenPacket(0) }, b.LookupIP)
	}
	if err == nil {
		tn.Shake()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err == nil {
		te.cfg = cfg
		// A new server: the old forwarded port is gone with the old tunnel.
		te.link, te.sig, te.pfPort, te.boundAt, te.pfErr = link, nil, 0, time.Time{}, ""
		te.pmp = pmpState{} // asked again on this start
	}
	te.starting = false
	if t.tunnels[id] != te || t.ctx.Err() != nil { // removed, replaced or closed meanwhile
		if tn != nil {
			tn.Close()
		}
		return
	}
	if err != nil {
		log.Printf("tunnel for %s: %v", id, err)
		te.err, te.retryAt = err.Error(), time.Now().Add(tunnelRetry)
		return
	}
	te.t, te.on, te.started, te.err = tn, b, time.Now(), ""
	te.gen++
	te.client = netif.NewHTTPClient(tn.DialContext)
}

// upLocked is the running tunnel of an interface, nil unless it is up and the
// interface is usable.
func (t *Tunnels) upLocked(ifaceID string) *entry {
	te := t.tunnels[ifaceID]
	ifc := t.ifaces[ifaceID]
	if te == nil || te.t == nil || !te.up || !ifc.Usable {
		return nil
	}
	return te
}

// Tunnel is a tunnel that was up when Up returned it. If it goes down later,
// its dials fail.
type Tunnel struct {
	Gen          int // its start: sockets of an older one are on a dead stack
	Port         int // where torrent peers come in: forwarded, typed in, or 0 (any)
	Client       *http.Client
	Dial         func(ctx context.Context, network, addr string) (net.Conn, error)
	LookupIP     func(ctx context.Context, host string) ([]net.IP, error)
	Listen       func(port int) (net.Listener, error)
	ListenPacket func(port int) (net.PacketConn, error)
}

// Up is the tunnel of an interface, if it is up and the interface usable.
func (t *Tunnels) Up(ifaceID string) (Tunnel, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	te := t.upLocked(ifaceID)
	if te == nil {
		return Tunnel{}, false
	}
	tn := te.t
	return Tunnel{
		Gen: te.gen, Port: t.portLocked(ifaceID, te), Client: te.client,
		Dial: tn.DialContext, LookupIP: tn.LookupIP, Listen: tn.Listen, ListenPacket: tn.ListenPacket,
	}, true
}

// Status is a tunnel as the settings page shows it.
type Status struct {
	Iface         string
	State         string // "up", "connecting", "down" (retrying), "off" (its interface isn't used)
	Err           string
	Provider      string // "conf" or "pia"
	Address       string // inside the tunnel; "" if not known
	Incoming      string // "reachable", "forwarded" (typed in, unchecked), "outgoing", "" (not known yet)
	ForwardedPort int    // typed in by the user
	SocksPort     int
}

// Status is every configured tunnel as of now.
func (t *Tunnels) Status(now time.Time) []Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Status
	for id, te := range t.tunnels {
		ifc := t.ifaces[id]
		state := "connecting"
		switch {
		case te.t != nil && te.up:
			state = "up"
		case !ifc.Usable:
			state = "off"
		case te.err != "" && te.t == nil:
			state = "down"
		}
		s := Status{Iface: id, State: state, Err: te.err, Provider: "conf", SocksPort: te.socks.Port()}
		if te.pia {
			s.Provider = "pia"
		}
		s.ForwardedPort = t.manualPortLocked(id, te)
		if te.cfg.Address.IsValid() {
			s.Address = te.cfg.Address.String()
		}
		if state == "up" {
			switch {
			case s.ForwardedPort != 0:
				s.Incoming = "forwarded" // the engine can't check a typed-in port
			case !te.pia && pmpReachableLocked(te, now):
				s.Incoming = "reachable"
			case !te.pia && (te.pmp.gen != te.gen || !te.pmp.probed):
				// Not known until its gateway is asked about NAT-PMP.
			case !te.pia || !te.link.forwards:
				s.Incoming = "outgoing"
			case te.pfPort != 0 && now.Sub(te.boundAt) < pfShown:
				s.Incoming = "reachable"
			case te.pfErr != "":
				s.Incoming = "outgoing"
			}
		}
		out = append(out, s)
	}
	return out
}

// manualPortLocked is the port the user typed in for an imported tunnel, 0 if
// none (or the tunnel is PIA's, which forwards its own).
func (t *Tunnels) manualPortLocked(ifaceID string, te *entry) int {
	if te.pia {
		return 0
	}
	return t.ifaces[ifaceID].ForwardedPort
}

// portLocked is the port a tunnel's torrent listener uses: the one typed in,
// else the one PIA or NAT-PMP forwarded (0: any).
func (t *Tunnels) portLocked(ifaceID string, te *entry) int {
	if p := t.manualPortLocked(ifaceID, te); p != 0 {
		return p
	}
	return te.pfPort
}
