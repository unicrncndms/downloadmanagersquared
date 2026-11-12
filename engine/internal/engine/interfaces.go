package engine

import (
	"context"
	"errors"
	"log"
	"net"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/netif"
	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/portmap"
	"downloadmanagersquared/engine/internal/rate"
	"downloadmanagersquared/engine/internal/socks"
	"downloadmanagersquared/engine/internal/torrent"
)

type ifaceEntry struct {
	info     netif.Interface
	binding  *netif.Binding
	gen      int          // goes up with each new binding, so torrents reopen their sockets on it
	shaper   *rate.Shaper // the interface's limits, across every socket its bindings hand out
	socks    *socks.Server
	present  bool
	incoming incoming
}

// incoming is whether torrent peers can connect in through an interface.
type incoming struct {
	checked   time.Time // last port mapping attempt; zero: not tried on this address yet
	reachable bool
}

const (
	portLease      = time.Hour
	portRenewAfter = 30 * time.Minute
	portRetryAfter = 5 * time.Minute
)

func (e *Engine) refreshInterfaces() {
	e.mu.Lock()
	ipv6 := e.settings.IPv6
	e.mu.Unlock()
	list, ipv6Only, err := netif.List(ipv6)
	if err != nil {
		log.Printf("listing interfaces: %v", err)
		return
	}
	e.mu.Lock()
	e.ipv6Only = ipv6Only
	e.refreshInterfacesLocked(list)
	e.mu.Unlock()
	e.tickVPN(time.Now())
	e.syncTorrentIfaces()
}

func (e *Engine) refreshInterfacesLocked(list []netif.Interface) {
	seen := map[string]bool{}
	nameChanged := false
	var ids []string
	for _, i := range list {
		seen[i.ID] = true
		ent := e.ifaces[i.ID]
		if ent == nil {
			srv, err := socks.Listen(e.socksPath(i.ID))
			if err != nil {
				log.Printf("socks for %s: %v", i.Name, err)
				continue
			}
			ent = &ifaceEntry{socks: srv, shaper: rate.NewShaper().Under(e.global)}
			e.ifaces[i.ID] = ent
			log.Printf("interface %s (%s) %s, socks port %d", i.Name, i.Description, strings.Join(addrs(i), " "), srv.Port())
		}
		if ent.binding == nil || !ent.info.IPv4.Equal(i.IPv4) || !ent.info.IPv6.Equal(i.IPv6) || ent.info.Index != i.Index || !slices.EqualFunc(ent.info.DNS, i.DNS, net.IP.Equal) {
			if ent.binding != nil {
				ent.binding.Close()
			}
			ent.binding = netif.NewBinding(i, ent.shaper)
			ent.gen++
			ent.incoming = incoming{} // a new address needs its own mapping
		}
		ent.info, ent.present = i, true
		s := e.settings.Interfaces[i.ID]
		if s == nil {
			s = &IfaceSettings{}
			e.settings.Interfaces[i.ID] = s
		}
		if s.Color == nil {
			ids = append(ids, i.ID)
		}
		if s.Name != i.Name {
			s.Name, nameChanged = i.Name, true
		}
	}
	for id, ent := range e.ifaces {
		if !seen[id] {
			ent.present = false
		}
	}
	// Interfaces without a colour (new ones, or every one for a user from before
	// colours) get theirs in the order the pages list them, by name, so an
	// existing user's colours stay as they were.
	sort.Slice(ids, func(a, b int) bool { return e.settings.Interfaces[ids[a]].Name < e.settings.Interfaces[ids[b]].Name })
	if e.settings.assignColors(ids) || nameChanged {
		e.saveSettingsLocked()
	}
	e.applyLimitsLocked()
}

// colorLocked is the palette index an interface is shown in.
func (e *Engine) colorLocked(id string) int {
	if s := e.settings.Interfaces[id]; s != nil && s.Color != nil {
		return *s.Color
	}
	return 0
}

// addrs are an interface's addresses, IPv4 first.
func addrs(i netif.Interface) []string {
	var out []string
	for _, ip := range []net.IP{i.IPv4, i.IPv6} {
		if ip != nil {
			out = append(out, ip.String())
		}
	}
	return out
}

func (e *Engine) applyLimitsLocked() {
	e.global.SetLimits(e.settings.UploadLimit, e.settings.DownloadLimit)
	for id, ent := range e.ifaces {
		var up, down int64
		if s := e.settings.Interfaces[id]; s != nil {
			up, down = s.UploadLimit, s.DownloadLimit
		}
		ent.shaper.SetLimits(up, down)
	}
}

// mapPorts keeps the torrent port forwarded on every interface's router.
func (e *Engine) mapPorts() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		e.refreshPortMaps(time.Now())
		select {
		case <-e.quit:
			return
		case <-t.C:
		}
	}
}

// refreshPortMaps asks each usable interface's router for the torrent port
// when its last answer is due for renewal or a retry.
func (e *Engine) refreshPortMaps(now time.Time) {
	type job struct {
		id string
		gw portmap.Gateway
	}
	var jobs []job
	// Only the no-VPN session listens on the interfaces, and only while one of
	// its torrents is in the swarm: otherwise there is nothing to open a port for.
	live := e.session(paths.Off).Live()
	e.mu.Lock()
	port := e.settings.TorrentPort
	for id, ent := range e.ifaces {
		in := &ent.incoming
		if !live {
			*in = incoming{}
			continue
		}
		if !ent.present || !e.enabledLocked(ent) {
			continue
		}
		if netif.PublicIPv4(ent.info.IPv4) { // no router in between to ask
			in.checked, in.reachable = now, true
			continue
		}
		wait := portRetryAfter
		if in.reachable {
			wait = portRenewAfter
		}
		if !in.checked.IsZero() && now.Sub(in.checked) < wait {
			continue
		}
		in.checked = now
		if ent.info.Gateway == nil {
			in.reachable = false
			continue
		}
		b := ent.binding
		jobs = append(jobs, job{id, portmap.Gateway{
			IP: ent.info.Gateway, Local: ent.info.IPv4, HTTP: b.HTTPClient(),
			Listen: func() (net.PacketConn, error) { return b.ListenPacket(0) },
		}})
	}
	e.mu.Unlock()
	for _, j := range jobs {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := e.mapPort(ctx, j.gw, port, portLease)
		cancel()
		if err != nil {
			log.Printf("torrent port on %s: %v", j.id, err)
		}
		e.mu.Lock()
		if ent := e.ifaces[j.id]; ent != nil && !ent.incoming.checked.IsZero() {
			ent.incoming.reachable = err == nil
		}
		e.mu.Unlock()
	}
}

// pathSource is the engine as paths.Open reaches interfaces and tunnels.
type pathSource struct{ e *Engine }

func (s pathSource) Binding(id string) (paths.Binding, error) {
	b, err := s.e.bindingOf(id)
	if err != nil { // not b itself: a nil *Binding in the interface would not be nil
		return nil, err
	}
	return b, nil
}

func (s pathSource) Tunnel(ifaceID string) (paths.Stack, bool) {
	tn, ok := s.e.vpn.Up(ifaceID)
	if !ok {
		return paths.Stack{}, false
	}
	return paths.Stack(tn), true
}

// Iface needs e.mu held.
func (s pathSource) Iface(id string) paths.Facts {
	f := paths.Facts{Conns: s.e.connsLocked(id), Upload: s.e.uploadLocked(id)}
	if ent := s.e.ifaces[id]; ent != nil && ent.present && ent.binding != nil {
		f.Gen, f.Binding = ent.gen, ent.binding
	}
	return f
}

// openPath is one path by ID, for a resolve's SOCKS proxy to use at the moment
// it needs it.
func (e *Engine) openPath(id string) (paths.Path, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return paths.Open(pathSource{e}, id)
}

// socksPath is a path as a resolve's SOCKS proxy sends its connections out.
func (e *Engine) socksPath(id string) socks.Path {
	return socks.Path{
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			p, err := e.openPath(id)
			if err != nil {
				return nil, err
			}
			return p.Dial(ctx, network, addr)
		},
		Lookup: func(ctx context.Context, host string) ([]net.IP, error) {
			p, err := e.openPath(id)
			if err != nil {
				return nil, err
			}
			return p.LookupIP(ctx, host)
		},
		IPv6: func() bool {
			p, err := e.openPath(id)
			return err == nil && p.IPv6()
		},
	}
}

func (e *Engine) bindingOf(id string) (*netif.Binding, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ent := e.ifaces[id]; ent != nil && ent.present {
		return ent.binding, nil
	}
	return nil, errors.New("interface is not connected")
}

// syncTorrentIfaces hands each torrent session the paths its VPN mode allows.
// Call it without e.mu held.
func (e *Engine) syncTorrentIfaces() {
	for m, s := range e.sessions {
		s.SetIfaces(e.torrentIfacesFor(m))
	}
}

// torrentIfacesFor are the usable paths under VPN mode m as torrents use them,
// main first (trackers and web seeds go through the first).
func (e *Engine) torrentIfacesFor(m paths.Mode) []torrent.Iface {
	e.mu.Lock()
	defer e.mu.Unlock()
	var ifs []torrent.Iface
	for _, p := range paths.Build(m, e.pathSnapshotLocked(), pathSource{e}) {
		ifs = append(ifs, torrent.Iface{
			ID: p.ID, Gen: p.Gen, Port: p.Port, Upload: p.Upload,
			Dial: p.Dial, ListenPacket: p.ListenPacket, LookupIP: p.LookupIP, Listen: p.Listen,
			ListenPacket6: p.ListenPacket6, Listen6: p.Listen6,
		})
	}
	return ifs
}

// uploadLocked is whether peers may download from us through the interface: on
// unless the user switched it off.
func (e *Engine) uploadLocked(id string) bool {
	if s := e.settings.Interfaces[id]; s != nil && s.Upload != nil {
		return *s.Upload
	}
	return true
}

// enabledLocked: every interface is used unless the user switched it off,
// except VPN adapters, which only the user switches on.
func (e *Engine) enabledLocked(ent *ifaceEntry) bool {
	if s := e.settings.Interfaces[ent.info.ID]; s != nil && s.Enabled != nil {
		return *s.Enabled
	}
	return !ent.info.VPN
}

func (e *Engine) connsLocked(id string) int {
	if s := e.settings.Interfaces[id]; s != nil && s.Connections > 0 {
		return s.Connections
	}
	return e.settings.DefaultConnections
}

// download.Env

// Paths are the paths a download in VPN mode m may use, the main path first.
func (e *Engine) Paths(m paths.Mode) []download.Iface {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []download.Iface
	for _, p := range paths.Build(m, e.pathSnapshotLocked(), pathSource{e}) {
		out = append(out, download.Iface{ID: p.ID, Client: p.Client, ByFamily: p.ByFamily, Conns: p.Conns})
	}
	return out
}

// pathSnapshotLocked is the interfaces and their tunnels as the path policy sees them.
func (e *Engine) pathSnapshotLocked() paths.Snapshot {
	s := paths.Snapshot{Main: e.settings.MainInterface}
	for id, ent := range e.ifaces {
		s.Ifaces = append(s.Ifaces, paths.Iface{
			ID: id, Present: ent.present, Enabled: e.enabledLocked(ent), Default: ent.info.IsDefault,
			TunnelUp: e.tunnelUpLocked(id),
		})
	}
	return s
}

// LineCheck is one interface's public IP, as Check lines found it.
type LineCheck struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	IP    string `json:"ip,omitempty"`
	Line  string `json:"line,omitempty"` // what is compared: the IPv4 address, or an IPv6 one's /64
	Error string `json:"error,omitempty"`
}

// CheckLines asks a public STUN server, through each enabled interface, which
// public IP it comes from: two with the same one share an uplink (a modem
// behind a mesh node, say). The only time DMS² contacts a third party for
// itself, so only when the user clicks.
func (e *Engine) CheckLines(server string) []LineCheck {
	type line struct {
		check LineCheck
		b     *netif.Binding
	}
	e.mu.Lock()
	var lines []line
	for id, ent := range e.ifaces {
		if ent.present && e.enabledLocked(ent) && ent.binding != nil {
			lines = append(lines, line{LineCheck{ID: id, Name: ent.info.Name}, ent.binding})
		}
	}
	e.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := range lines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ip, err := lines[i].b.PublicIP(ctx, server)
			if err != nil {
				lines[i].check.Error = err.Error()
			} else {
				lines[i].check.IP, lines[i].check.Line = ip.String(), netif.LineOf(ip)
			}
		}()
	}
	wg.Wait()
	out := make([]LineCheck, len(lines))
	for i, l := range lines {
		out[i] = l.check
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
