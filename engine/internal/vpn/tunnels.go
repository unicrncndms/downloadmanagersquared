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
	"errors"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sync"
	"time"

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

// Tunnels are the VPN tunnels of every interface. An interface with an
// imported config uses it; PIA, once signed in, serves the interfaces without one.
type Tunnels struct {
	dir    string
	ctx    context.Context // cancelled by Close; every job takes it
	cancel context.CancelFunc
	jobs   sync.WaitGroup

	// The providers. Their state is under mu.
	imported *imported
	pia      *piaProvider

	mu      sync.Mutex
	tunnels map[string]*entry // by interface ID
	ifaces  map[string]Iface  // as of the last Tick
}

// provider is where a tunnel's config comes from. Its methods are called with
// Tunnels.mu held, except config.
type provider interface {
	name() string // as Status shows it: "conf" or "pia"
	// ready is whether te may start now. A provider that says no either waits
	// for the user or has put the reason in te.err.
	ready(te *entry) bool
	// config is the config for a new start of te's tunnel through b, and the
	// forwarder of that start, given its running tunnel. Called without the lock.
	config(ctx context.Context, te *entry, b *netif.Binding) (tunnel.Config, func(*tunnel.Tunnel) forwarder, error)
	lostHint() string     // added to the error when the server stops answering
	takesTypedPort() bool // whether the user's typed-in port applies
}

// forwarder keeps a port forwarded to one start of a tunnel. It is dropped with
// that start. Its methods are called with Tunnels.mu held.
type forwarder interface {
	// forward starts the work due at now, in the background.
	forward(now time.Time)
	// port is the forwarded port (0: none) and whether peers can reach it at
	// now: "reachable", "outgoing", or "" while not known yet.
	port(now time.Time) (int, string)
}

// entry is the tunnel configured for one interface.
type entry struct {
	id       string
	prov     provider
	cfg      tunnel.Config // a .conf's, or PIA's latest
	socks    *socks.Server // landing pages resolved through the tunnel
	t        *tunnel.Tunnel
	on       *netif.Binding // what t runs over
	client   *http.Client
	fw       forwarder // this start's
	up       bool
	gen      int // counts starts: torrent sockets of an older one are on a dead stack
	starting bool
	started  time.Time
	retryAt  time.Time
	err      string
}

// stopLocked takes te's tunnel down, returning the device for the caller to
// close once the lock is released (nil if it wasn't running).
func (te *entry) stopLocked() *tunnel.Tunnel {
	tn := te.t
	te.t, te.on, te.client, te.fw, te.up = nil, nil, nil, nil, false
	return tn
}

func New(o Options) *Tunnels {
	if o.PIA.TokenURL == "" {
		o.PIA = pia.Default
	}
	if o.GatewayOK == nil {
		o.GatewayOK = privateGateway
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &Tunnels{
		dir: o.Dir, ctx: ctx, cancel: cancel,
		tunnels: map[string]*entry{}, ifaces: map[string]Iface{},
	}
	t.imported = &imported{t: t, gatewayOK: o.GatewayOK}
	t.pia = &piaProvider{t: t, api: o.PIA, region: o.PIARegion, servers: map[*entry]string{}}
	return t
}

// Load reads the saved configs and the PIA login. What can't be read is set
// aside (renamed .broken) and returned, for the engine to tell the user.
func (t *Tunnels) Load() []Problem {
	problems := t.imported.load()
	if err := t.pia.load(); err != nil {
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
	t.forward(now)
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

// goJob runs f as background work, which takes t.ctx.
func (t *Tunnels) goJob(f func()) {
	t.jobs.Add(1)
	go func() {
		defer t.jobs.Done()
		f()
	}()
}

// addEntry makes a tunnel of prov for an interface, replacing any it had.
func (t *Tunnels) addEntry(ifaceID string, prov provider, cfg tunnel.Config, err string) error {
	t.removeEntry(ifaceID)
	srv, lerr := socks.Listen(t.socksPath(ifaceID))
	if lerr != nil {
		return lerr
	}
	t.mu.Lock()
	t.tunnels[ifaceID] = &entry{id: ifaceID, prov: prov, cfg: cfg, socks: srv, err: err}
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

func (t *Tunnels) removeEntry(ifaceID string) {
	t.mu.Lock()
	te := t.tunnels[ifaceID]
	delete(t.tunnels, ifaceID)
	var tn *tunnel.Tunnel
	if te != nil {
		tn = te.stopLocked()
	}
	t.mu.Unlock()
	if te != nil {
		te.socks.Close()
	}
	if tn != nil {
		tn.Close()
	}
}

// sync runs a tunnel for each interface that has one and is usable, following
// the interface's address, and restarts a tunnel whose server stopped
// answering, like an unreachable interface retrying.
func (t *Tunnels) sync(now time.Time) {
	type start struct {
		te *entry
		b  *netif.Binding
	}
	t.pia.addWanted()

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
					te.err, te.retryAt = "the VPN server doesn't answer"+te.prov.lostHint(), now.Add(tunnelRetry)
				}
				stops = append(stops, te.stopLocked())
			}
		}
		if usable && te.t == nil && !te.starting && !now.Before(te.retryAt) && te.prov.ready(te) {
			te.starting = true
			starts = append(starts, start{te, ifc.Binding})
		}
	}
	t.mu.Unlock()
	for _, tn := range stops {
		tn.Close()
	}
	for _, s := range starts {
		t.goJob(func() { t.startTunnel(s.te, s.b) })
	}
}

func (t *Tunnels) startTunnel(te *entry, b *netif.Binding) {
	cfg, newFw, err := te.prov.config(t.ctx, te, b)
	var tn *tunnel.Tunnel
	if err == nil {
		tn, err = tunnel.Start(cfg, func() (net.PacketConn, error) { return b.ListenPacket(0) }, b.LookupIP)
	}
	if err == nil {
		tn.Shake()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	te.starting = false
	if t.tunnels[te.id] != te || t.ctx.Err() != nil { // removed, replaced or closed meanwhile
		if tn != nil {
			tn.Close()
		}
		return
	}
	if err != nil {
		log.Printf("tunnel for %s: %v", te.id, err)
		te.err, te.retryAt = err.Error(), time.Now().Add(tunnelRetry)
		return
	}
	te.cfg = cfg
	te.t, te.on, te.started, te.err = tn, b, time.Now(), ""
	te.gen++
	te.client = netif.NewHTTPClient(tn.DialContext)
	te.fw = newFw(tn)
}

// forward keeps the ports of the tunnels that are up forwarded. A port the
// user typed in wins: its tunnel's forwarder isn't asked.
func (t *Tunnels) forward(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, te := range t.tunnels {
		if t.upLocked(id) == nil || t.typedPortLocked(te) != 0 {
			continue
		}
		te.fw.forward(now)
	}
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
	port, _ := t.portLocked(te, time.Now())
	return Tunnel{
		Gen: te.gen, Port: port, Client: te.client,
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
		state := "connecting"
		switch {
		case te.t != nil && te.up:
			state = "up"
		case !t.ifaces[id].Usable:
			state = "off"
		case te.err != "" && te.t == nil:
			state = "down"
		}
		s := Status{
			Iface: id, State: state, Err: te.err, Provider: te.prov.name(),
			ForwardedPort: t.typedPortLocked(te), SocksPort: te.socks.Port(),
		}
		if te.cfg.Address.IsValid() {
			s.Address = te.cfg.Address.String()
		}
		if state == "up" {
			_, s.Incoming = t.portLocked(te, now)
		}
		out = append(out, s)
	}
	return out
}

// typedPortLocked is the port the user typed in for te's tunnel, 0 if none or
// its provider forwards its own.
func (t *Tunnels) typedPortLocked(te *entry) int {
	if !te.prov.takesTypedPort() {
		return 0
	}
	return t.ifaces[te.id].ForwardedPort
}

// portLocked is the port a running tunnel's torrent listener uses, and whether
// peers reach it: the one typed in (which the engine can't check), else its
// forwarder's.
func (t *Tunnels) portLocked(te *entry, now time.Time) (int, string) {
	if p := t.typedPortLocked(te); p != 0 {
		return p, "forwarded"
	}
	return te.fw.port(now)
}
