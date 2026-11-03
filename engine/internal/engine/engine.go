// Package engine ties everything together: interfaces, downloads, the queue,
// persistence and the API that the extension (and later a GUI) talk to.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/ipc"
	"downloadmanagersquared/engine/internal/netif"
	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/pia"
	"downloadmanagersquared/engine/internal/portmap"
	"downloadmanagersquared/engine/internal/rate"
	"downloadmanagersquared/engine/internal/socks"
	"downloadmanagersquared/engine/internal/torrent"
	"downloadmanagersquared/engine/internal/winutil"
)

var ErrAlreadyRunning = errors.New("engine already running")

const Version = "0.1.0"

const defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0"

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

type client struct {
	extension bool
	seq       int64 // higher = connected later
}

type resolveReq struct {
	ID        string `json:"id"`
	Iface     string `json:"iface"`
	IfaceName string `json:"ifaceName"`
	PageURL   string `json:"pageUrl"`
	SocksPort int    `json:"socksPort"`
}

// entry is a download of either kind, as the list and the queue see it.
type entry interface {
	ID() string
	Status() download.Status
	Running() bool
	Start()
	Stop(as download.Status)
	Path() string
	Summary() download.Summary
	VPN() paths.Mode
	PauseForVPN()
	WaitingForVPN() bool
}

type Engine struct {
	dir string
	ln  net.Listener
	// One torrent session per VPN mode: a session's sockets are only the paths
	// its mode allows, so a VPN-only torrent has no bare socket to leak through.
	sessions map[paths.Mode]*torrent.Session

	mu        sync.Mutex
	settings  Settings
	ifaces    map[string]*ifaceEntry
	downloads map[string]entry
	order     []string
	dirty     map[string]bool
	clients   map[*ipc.Conn]*client
	clientSeq int64
	tunnels   map[string]*tunnelEntry // by interface ID
	resolves  map[string]resolveReq   // waiting for an extension to pick them up
	openWant  *string                 // view to open once Firefox connects ("#settings" or downloads)

	cum      float64 // bytes moved since start, integrated from speed (for the global ETA)
	meter    rate.Meter
	eta      rate.ETA
	snapshot StateEvent

	mapPort func(ctx context.Context, gw portmap.Gateway, port int, lifetime time.Duration) error
	pia     *piaState
	piaAPI  pia.API

	quit     chan struct{}
	quitOnce sync.Once
}

// New claims the pipe (proving this is the only engine) and loads saved state.
func New() (*Engine, error) {
	ln, err := ipc.Listen()
	if err != nil {
		return nil, ErrAlreadyRunning
	}
	base, err := os.UserConfigDir()
	if err != nil {
		ln.Close()
		return nil, err
	}
	e, err := open(filepath.Join(base, "downloadmanagersquared"), ln)
	if err != nil {
		ln.Close()
		return nil, err
	}
	e.refreshInterfaces()
	e.updateSnapshot(0)
	return e, nil
}

// open loads the engine kept in dir, with no interfaces yet. ln may be nil.
func open(dir string, ln net.Listener) (*Engine, error) {
	os.MkdirAll(filepath.Join(dir, "downloads"), 0o755)
	os.MkdirAll(filepath.Join(dir, "torrents"), 0o755)
	e := &Engine{
		dir:       dir,
		ln:        ln,
		settings:  loadSettings(filepath.Join(dir, "settings.json")),
		ifaces:    map[string]*ifaceEntry{},
		downloads: map[string]entry{},
		dirty:     map[string]bool{},
		clients:   map[*ipc.Conn]*client{},
		resolves:  map[string]resolveReq{},
		tunnels:   map[string]*tunnelEntry{},
		mapPort:   portmap.Map,
		piaAPI:    pia.Default,
		quit:      make(chan struct{}),
	}
	e.saveSettingsLocked() // keeps the torrent port picked on the first start
	e.sessions = map[paths.Mode]*torrent.Session{}
	// Alongside listens nowhere: the other two hold the torrent port on the
	// interfaces and in the tunnels.
	ports := map[paths.Mode]int{paths.Off: e.settings.TorrentPort, paths.VPNOnly: e.settings.TorrentPort}
	for _, m := range []paths.Mode{paths.Off, paths.Alongside, paths.VPNOnly} {
		s, err := torrent.NewSession(torrent.Config{
			Changed:  func(t *torrent.Torrent) { e.markDirty(t.ID()) },
			Finished: func(t *torrent.Torrent) { e.notifyFinished(t.Summary()) },
			Ratio: func() float64 {
				e.mu.Lock()
				defer e.mu.Unlock()
				return e.settings.SeedRatio
			},
			Port: ports[m],
		})
		if err != nil {
			for _, s := range e.sessions {
				s.Close()
			}
			return nil, err
		}
		e.sessions[m] = s
	}
	e.loadDownloads()
	e.loadTunnels()
	e.loadPIA()
	return e, nil
}

func (e *Engine) Dir() string { return e.dir }

// session runs the torrents of VPN mode m.
func (e *Engine) session(m paths.Mode) *torrent.Session {
	if s := e.sessions[m]; s != nil {
		return s
	}
	return e.sessions[paths.Off]
}

// loadDownloads restores both kinds, in the order they were added.
func (e *Engine) loadDownloads() {
	type loaded struct {
		j     entry
		added time.Time
	}
	var all []loaded
	for _, st := range readStates[download.State](filepath.Join(e.dir, "downloads")) {
		if st.Status == download.StatusRunning || st.Status == download.StatusVerifying {
			st.Status = download.StatusQueued
		}
		st.VPN = paths.ParseMode(string(st.VPN), paths.Off) // saved before modes were spelled out
		all = append(all, loaded{download.New(e, st), st.Added})
	}
	for _, st := range readStates[torrent.State](filepath.Join(e.dir, "torrents")) {
		// Seeding and fetching metadata go on (schedule restarts them); the rest queue again.
		switch st.Status {
		case download.StatusRunning, torrent.StatusChecking:
			st.Status = download.StatusQueued
		}
		st.VPN = paths.ParseMode(string(st.VPN), paths.Off)
		t, err := e.session(st.VPN).Add(st)
		if err != nil {
			log.Printf("skipping torrent %s: %v", st.ID, err)
			continue
		}
		all = append(all, loaded{t, st.Added})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].added.Before(all[j].added) })
	for _, l := range all {
		e.downloads[l.j.ID()] = l.j
		e.order = append(e.order, l.j.ID())
	}
}

// readStates reads every saved state in dir that has an ID.
func readStates[S interface{ download.State | torrent.State }](dir string) []S {
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var out []S
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var st S
		var id struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(b, &st) != nil || json.Unmarshal(b, &id) != nil || id.ID == "" {
			log.Printf("skipping unreadable state %s", f)
			continue
		}
		out = append(out, st)
	}
	return out
}

// Run serves the API and drives the queue until Shutdown.
func (e *Engine) Run() {
	go ipc.Serve(e.ln, e)
	go e.mapPorts()
	go e.forwardLoop()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	last := time.Now()
	n := 0
	for {
		select {
		case <-e.quit:
			return
		case now := <-t.C:
			n++
			if n%10 == 0 {
				e.refreshInterfaces()
			}
			e.syncTunnels(now)
			e.syncTorrentIfaces()
			e.schedule()
			e.persist()
			e.updateSnapshot(now.Sub(last))
			last = now
			e.broadcast("state", e.Snapshot())
		}
	}
}

func (e *Engine) Shutdown() {
	e.quitOnce.Do(func() {
		close(e.quit)
		if e.ln != nil {
			e.ln.Close()
		}
		for _, j := range e.list() {
			if j.Running() {
				j.Stop(download.StatusQueued)
			}
			e.markDirty(j.ID())
		}
		e.persist()
		for _, s := range e.sessions {
			s.Close()
		}
		e.closeTunnels()
		e.mu.Lock()
		for _, ent := range e.ifaces {
			ent.socks.Close()
		}
		e.mu.Unlock()
	})
}

func (e *Engine) list() []entry {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]entry, 0, len(e.order))
	for _, id := range e.order {
		out = append(out, e.downloads[id])
	}
	return out
}

func (e *Engine) entry(id string) (entry, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	j := e.downloads[id]
	if j == nil {
		return nil, fmt.Errorf("no download %q", id)
	}
	return j, nil
}

// get is for what only HTTP downloads have: links, hashes, landing pages.
func (e *Engine) get(id string) (*download.Download, error) {
	j, err := e.entry(id)
	if err != nil {
		return nil, err
	}
	d, ok := j.(*download.Download)
	if !ok {
		return nil, fmt.Errorf("%q is not an HTTP download", id)
	}
	return d, nil
}

// schedule starts queued downloads while there are free slots, HTTP downloads
// before torrents, each in the order added. A queued HTTP download that finds
// no free slot takes one from the latest added torrent that is downloading,
// which goes back to the queue.
func (e *Engine) schedule() {
	all := e.list()
	e.mu.Lock()
	limit := e.settings.MaxParallel
	snap := e.pathSnapshotLocked()
	e.mu.Unlock()
	running := 0
	var queued, torrents []entry
	var downloading []*torrent.Torrent // holding a slot, in the order added
	for _, d := range all {
		t, isTorrent := d.(*torrent.Torrent)
		if paths.For(d.VPN(), snap).Wait {
			d.PauseForVPN() // never without a tunnel: under VPN only there is no other path
			if d.WaitingForVPN() {
				continue
			}
		} else if d.WaitingForVPN() {
			e.requeue(d) // a tunnel came up
		}
		switch {
		case d.Running():
			running++
			if isTorrent {
				downloading = append(downloading, t)
			}
		case isTorrent && !t.Active() && (t.Status() == torrent.StatusSeeding || t.Status() == torrent.StatusMetadata):
			// Seeding and fetching metadata hold no slot: a torrent restored doing either rejoins at once.
			t.Start()
		case d.Status() == download.StatusQueued && isTorrent:
			torrents = append(torrents, d)
		case d.Status() == download.StatusQueued:
			queued = append(queued, d)
		}
	}
	for _, d := range append(queued, torrents...) {
		if running >= limit {
			if _, isTorrent := d.(*torrent.Torrent); isTorrent || len(downloading) == 0 {
				return
			}
			downloading[len(downloading)-1].Stop(download.StatusQueued)
			downloading = downloading[:len(downloading)-1]
			running--
		}
		d.Start()
		if d.Running() { // a torrent still to be chosen only fetches its files
			running++
		}
	}
}

func (e *Engine) persist() {
	e.mu.Lock()
	ids := make([]string, 0, len(e.dirty))
	for id := range e.dirty {
		ids = append(ids, id)
	}
	e.dirty = map[string]bool{}
	e.mu.Unlock()
	for _, id := range ids {
		j, err := e.entry(id)
		if err != nil {
			continue
		}
		var st any
		switch j := j.(type) {
		case *download.Download:
			st = j.State()
		case *torrent.Torrent:
			st = j.State()
		}
		if err := writeJSON(e.statePath(j), st); err != nil {
			log.Printf("saving %s: %v", id, err)
		}
	}
}

func (e *Engine) statePath(j entry) string {
	kind := "downloads"
	if _, ok := j.(*torrent.Torrent); ok {
		kind = "torrents"
	}
	return filepath.Join(e.dir, kind, j.ID()+".json")
}

func (e *Engine) saveSettingsLocked() {
	if err := writeJSON(filepath.Join(e.dir, "settings.json"), e.settings); err != nil {
		log.Printf("saving settings: %v", err)
	}
}

func (e *Engine) refreshInterfaces() {
	list, err := netif.List()
	if err != nil {
		log.Printf("listing interfaces: %v", err)
		return
	}
	e.mu.Lock()
	e.refreshInterfacesLocked(list)
	e.mu.Unlock()
	e.syncTorrentIfaces()
}

func (e *Engine) refreshInterfacesLocked(list []netif.Interface) {
	seen := map[string]bool{}
	nameChanged := false
	for _, i := range list {
		seen[i.ID] = true
		ent := e.ifaces[i.ID]
		if ent == nil {
			srv, err := socks.Listen(e.dialVia(i.ID))
			if err != nil {
				log.Printf("socks for %s: %v", i.Name, err)
				continue
			}
			ent = &ifaceEntry{socks: srv, shaper: rate.NewShaper()}
			e.ifaces[i.ID] = ent
			log.Printf("interface %s (%s) %s, socks port %d", i.Name, i.Description, i.IPv4, srv.Port())
		}
		if ent.binding == nil || !ent.info.IPv4.Equal(i.IPv4) || ent.info.Index != i.Index || !slices.EqualFunc(ent.info.DNS, i.DNS, net.IP.Equal) {
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
		if s.Name != i.Name {
			s.Name, nameChanged = i.Name, true
		}
	}
	for id, ent := range e.ifaces {
		if !seen[id] {
			ent.present = false
		}
	}
	if nameChanged {
		e.saveSettingsLocked()
	}
	e.applyLimitsLocked()
}

func (e *Engine) applyLimitsLocked() {
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
		if publicIPv4(ent.info.IPv4) { // no router in between to ask
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

// publicIPv4 is whether ip is reachable from the internet as it is: not private,
// shared (carrier NAT), loopback or link-local.
func publicIPv4(ip net.IP) bool {
	cgnat := net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}
	return ip.To4() != nil && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !cgnat.Contains(ip)
}

// dialVia dials through a path: an interface, or its tunnel.
func (e *Engine) dialVia(id string) socks.DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if ifaceID, ok := paths.IfaceOf(id); ok {
			e.mu.Lock()
			te := e.tunnelUpLocked(ifaceID)
			e.mu.Unlock()
			if te == nil {
				return nil, errors.New("the VPN tunnel is not up")
			}
			return te.t.DialContext(ctx, network, addr)
		}
		b, err := e.bindingOf(id)
		if err != nil {
			return nil, err
		}
		return b.DialContext(ctx, network, addr)
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
	for _, id := range paths.For(m, e.pathSnapshotLocked()).Paths {
		if base, tun := paths.IfaceOf(id); tun {
			// Bound to this tunnel's stack: after a restart it is a new generation.
			te := e.tunnelUpLocked(base)
			t := te.t
			ifs = append(ifs, torrent.Iface{
				ID: id, Gen: te.gen, Port: te.pfPort, Upload: e.uploadLocked(base),
				Dial: t.DialContext, ListenPacket: t.ListenPacket, LookupIP: t.LookupIP, Listen: t.Listen,
			})
			continue
		}
		ifs = append(ifs, torrent.Iface{
			ID:     id,
			Gen:    e.ifaces[id].gen, // a new address: the old sockets are bound to the old one
			Upload: e.uploadLocked(id),
			Dial:   e.dialVia(id),
			ListenPacket: func(port int) (net.PacketConn, error) {
				b, err := e.bindingOf(id)
				if err != nil {
					return nil, err
				}
				return b.ListenPacket(port)
			},
			Listen: func(port int) (net.Listener, error) {
				b, err := e.bindingOf(id)
				if err != nil {
					return nil, err
				}
				return b.Listen(port)
			},
			LookupIP: func(ctx context.Context, host string) ([]net.IP, error) {
				b, err := e.bindingOf(id)
				if err != nil {
					return nil, err
				}
				return b.LookupIP(ctx, host)
			},
		})
	}
	return ifs
}

// uploadLocked is whether peers may download from us through the interface.
func (e *Engine) uploadLocked(id string) bool {
	if s := e.settings.Interfaces[id]; s != nil && s.Upload != nil {
		return *s.Upload
	}
	ent := e.ifaces[id]
	return ent == nil || !ent.info.Metered
}

func (e *Engine) enabledLocked(ent *ifaceEntry) bool {
	if s := e.settings.Interfaces[ent.info.ID]; s != nil && s.Enabled != nil {
		return *s.Enabled
	}
	return !ent.info.Metered && !ent.info.VPN
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
	for _, id := range paths.For(m, e.pathSnapshotLocked()).Paths {
		base, tun := paths.IfaceOf(id)
		var client *http.Client
		if tun {
			client = e.tunnelUpLocked(base).client
		} else {
			client = e.ifaces[id].binding.HTTPClient()
		}
		out = append(out, download.Iface{ID: id, Client: client, Conns: e.connsLocked(base)})
	}
	return out
}

// pathSnapshotLocked is the interfaces and their tunnels as the path policy sees them.
func (e *Engine) pathSnapshotLocked() paths.Snapshot {
	s := paths.Snapshot{Main: e.settings.MainInterface}
	for id, ent := range e.ifaces {
		s.Ifaces = append(s.Ifaces, paths.Iface{
			ID: id, Present: ent.present, Enabled: e.enabledLocked(ent), Default: ent.info.IsDefault,
			TunnelUp: e.tunnelUpLocked(id) != nil,
		})
	}
	return s
}

func (e *Engine) ETAWindow() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return time.Duration(e.settings.ETAWindowSec) * time.Second
}

func (e *Engine) RequestResolve(id, ifaceID, pageURL string) {
	e.mu.Lock()
	req := resolveReq{ID: id, Iface: ifaceID, PageURL: pageURL}
	if base, tun := paths.IfaceOf(ifaceID); tun {
		if te, ent := e.tunnels[base], e.ifaces[base]; te != nil && ent != nil {
			req.IfaceName, req.SocksPort = "VPN via "+ent.info.Name, te.socks.Port()
		}
	} else if ent := e.ifaces[ifaceID]; ent != nil {
		req.IfaceName, req.SocksPort = ent.info.Name, ent.socks.Port()
	}
	e.resolves[id+"|"+ifaceID] = req
	c := e.extensionLocked()
	e.mu.Unlock()
	log.Printf("resolve %s via %s", id, req.IfaceName)
	if c != nil {
		c.Emit("resolve", req)
	}
}

func (e *Engine) Changed(d *download.Download) { e.markDirty(d.ID()) }

func (e *Engine) markDirty(id string) {
	e.mu.Lock()
	e.dirty[id] = true
	e.mu.Unlock()
}

func (e *Engine) Finished(d *download.Download) { e.notifyFinished(d.Summary()) }

// notify shows a Windows notification; tests switch it off.
var notify = winutil.Notify

func (e *Engine) notifyFinished(s download.Summary) {
	if s.Status == download.StatusCompleted || s.Status == torrent.StatusSeeding {
		notify("Download complete", s.Filename)
	} else {
		notify("Download failed", s.Filename+": "+s.Error)
	}
}

// extensionLocked is the most recently connected browser extension.
func (e *Engine) extensionLocked() *ipc.Conn {
	var best *ipc.Conn
	var seq int64 = -1
	for c, cl := range e.clients {
		if cl.extension && cl.seq > seq {
			best, seq = c, cl.seq
		}
	}
	return best
}

func (e *Engine) broadcast(name string, data any) {
	e.mu.Lock()
	cs := make([]*ipc.Conn, 0, len(e.clients))
	for c := range e.clients {
		cs = append(cs, c)
	}
	e.mu.Unlock()
	if len(cs) == 0 {
		return
	}
	b, err := ipc.EncodeEvent(name, data)
	if err != nil {
		return
	}
	for _, c := range cs {
		c.WriteRaw(b)
	}
}

// OpenManager shows the downloads (or, with "#settings", the settings page) in
// Firefox, starting Firefox if needed.
func (e *Engine) OpenManager(hash string) {
	e.mu.Lock()
	c := e.extensionLocked()
	if c == nil {
		e.openWant = &hash
	}
	e.mu.Unlock()
	if c != nil {
		c.Emit("openManager", map[string]string{"hash": hash})
		return
	}
	if err := winutil.LaunchFirefox(); err != nil {
		log.Printf("starting firefox: %v", err)
	}
}

func (e *Engine) PauseAll() {
	for _, d := range e.list() {
		d.Stop(download.StatusPaused)
	}
}

func (e *Engine) ResumeAll() {
	for _, d := range e.list() {
		if s := d.Status(); s == download.StatusPaused || s == download.StatusFailed {
			e.requeue(d)
		}
	}
}

// chooseTorrentVPN answers a new torrent's VPN question, moving it to the
// session of that mode before it joins the swarm.
func (e *Engine) chooseTorrentVPN(id, vpn string) (*torrent.Torrent, error) {
	j, err := e.entry(id)
	if err != nil {
		return nil, err
	}
	t, ok := j.(*torrent.Torrent)
	if !ok || !t.State().AskVPN || t.Active() {
		return nil, fmt.Errorf("%q isn't waiting for a VPN choice", id)
	}
	st := t.State()
	st.VPN, st.AskVPN, st.Status = paths.ParseMode(vpn, paths.Off), false, download.StatusQueued
	nt, err := e.session(st.VPN).Add(st)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	if e.downloads[id] != j {
		e.mu.Unlock()
		return nil, fmt.Errorf("%q changed meanwhile", id)
	}
	e.downloads[id], e.dirty[id] = nt, true
	e.mu.Unlock()
	nt.Start() // learn the files through the chosen paths, or back to choosing them
	return nt, nil
}

// requeue lets a stopped download run again: queued, or for a torrent whose
// download completed, seeding, which takes no queue slot (schedule restarts it).
func (e *Engine) requeue(j entry) {
	if t, ok := j.(*torrent.Torrent); ok && !t.State().Finished.IsZero() {
		t.Stop(torrent.StatusSeeding)
		return
	}
	j.Stop(download.StatusQueued)
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type AddRequest struct {
	URL       string `json:"url"`
	PageURL   string `json:"pageUrl"`
	Filename  string `json:"filename"`
	Cookie    string `json:"cookie"`
	Referer   string `json:"referer"`
	UserAgent string `json:"userAgent"`
	Torrent   bool   `json:"torrent"` // the URL serves a .torrent file
	VPN       string `json:"vpn"`     // "off", "alongside" or "vpn"; "" = the default for its type
}

func (e *Engine) Add(r AddRequest) (string, error) {
	r.URL = strings.TrimSpace(r.URL)
	if strings.HasPrefix(r.URL, "magnet:") {
		id, err := e.addTorrent(torrent.State{Magnet: r.URL}, r.VPN)
		if err != nil {
			return "", fmt.Errorf("not a usable magnet link: %w", err)
		}
		return id, nil
	}
	if !strings.HasPrefix(r.URL, "http://") && !strings.HasPrefix(r.URL, "https://") {
		return "", errors.New("only http, https and magnet links are supported")
	}
	if r.UserAgent == "" {
		// Some servers drop clients with Go's default user agent.
		r.UserAgent = defaultUserAgent
	}
	if r.Torrent || isTorrentURL(r.URL) {
		return e.addTorrentURL(r)
	}
	e.mu.Lock()
	st := download.State{
		VPN: paths.ParseMode(r.VPN, e.settings.VPNHTTP),
		ID:  newID(), URL: r.URL, PageURL: strings.TrimSpace(r.PageURL),
		Cookie: r.Cookie, Referer: r.Referer, UserAgent: r.UserAgent,
		Dir: e.settings.SaveDir, Filename: download.SanitizeFilename(r.Filename),
		Size: -1, Status: download.StatusQueued, Added: time.Now(),
	}
	d := download.New(e, st)
	e.downloads[st.ID] = d
	e.order = append(e.order, st.ID)
	e.dirty[st.ID] = true
	e.mu.Unlock()
	log.Printf("added %s", st.ID) // no URL: links are private
	return st.ID, nil
}

func isTorrentURL(u string) bool {
	p, err := url.Parse(u)
	return err == nil && strings.HasSuffix(strings.ToLower(p.Path), ".torrent")
}

const maxTorrentFile = 16 << 20

// fetchTorrentFile downloads a .torrent file the way the browser would have.
func fetchTorrentFile(ctx context.Context, c *http.Client, r AddRequest) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range map[string]string{"Cookie": r.Cookie, "Referer": r.Referer, "User-Agent": r.UserAgent} {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the torrent file's server answered %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxTorrentFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxTorrentFile {
		return nil, errors.New("too big for a torrent file")
	}
	return b, nil
}

// addTorrentURL fetches a .torrent file through the main interface and adds it.
func (e *Engine) addTorrentURL(r AddRequest) (string, error) {
	// Fetched the way the torrent will run: under VPN only, through a tunnel.
	e.mu.Lock()
	m := paths.ParseMode(r.VPN, e.settings.VPNTorrent)
	e.mu.Unlock()
	ps := e.Paths(m)
	if len(ps) == 0 {
		return "", errors.New("no connection to fetch the torrent file through")
	}
	b, err := fetchTorrentFile(context.Background(), ps[0].Client, r)
	if err != nil {
		return "", err
	}
	return e.addTorrentFile(b, r.VPN)
}

func (e *Engine) addTorrentFile(b []byte, vpn string) (string, error) {
	magnet, info, err := torrent.ParseFile(b)
	if err != nil {
		return "", err
	}
	return e.addTorrent(torrent.State{Magnet: magnet, Info: info}, vpn)
}

// addTorrent adds a torrent that waits for the user's choice of files, under
// VPN mode vpn ("" = the default for torrents).
func (e *Engine) addTorrent(st torrent.State, vpn string) (string, error) {
	e.mu.Lock()
	st.Dir = e.settings.SaveDir
	st.VPN = paths.ParseMode(vpn, e.settings.VPNTorrent)
	// About to join the swarm on the bare connection, unasked: suggest a VPN first.
	st.AskVPN = vpn == "" && st.VPN == paths.Off
	e.mu.Unlock()
	st.ID, st.Choose, st.Added = newID(), true, time.Now()
	t, err := e.session(st.VPN).Add(st)
	if err != nil {
		return "", err
	}
	t.Start() // learn the files for the add step, without waiting for a slot
	e.mu.Lock()
	e.downloads[t.ID()] = t
	e.order = append(e.order, t.ID())
	e.dirty[t.ID()] = true
	e.mu.Unlock()
	log.Printf("added torrent %s", t.ID())
	return t.ID(), nil
}

func (e *Engine) Remove(id string, deleteFile bool) error {
	d, err := e.entry(id)
	if err != nil {
		return err
	}
	// Once a torrent's download completed (seeding, or checking to seed again), its files are the user's.
	finished := d.Status() == download.StatusCompleted
	if t, ok := d.(*torrent.Torrent); ok && !t.State().Finished.IsZero() {
		finished = true
	}
	d.Stop(download.StatusPaused)
	if p := d.Path(); deleteFile && p != "" && !finished {
		if _, ok := d.(*torrent.Torrent); ok {
			os.RemoveAll(p)        // a multi-file torrent is a folder
			os.Remove(p + ".part") // a single file waits under .part until complete
		} else {
			os.Remove(p)
		}
	}
	e.mu.Lock()
	delete(e.downloads, id)
	delete(e.dirty, id)
	for k := range e.resolves {
		if strings.HasPrefix(k, id+"|") {
			delete(e.resolves, k)
		}
	}
	e.order = slices.DeleteFunc(e.order, func(x string) bool { return x == id })
	e.mu.Unlock()
	os.Remove(e.statePath(d))
	return nil
}

type IfaceView struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	IP          string  `json:"ip"`
	Present     bool    `json:"present"`
	Enabled     bool    `json:"enabled"`
	Metered     bool    `json:"metered"`
	VPN         bool    `json:"vpn"`
	IsDefault   bool    `json:"isDefault"`
	IsMain      bool    `json:"isMain"`
	Connections int     `json:"connections"`
	SocksPort   int     `json:"socksPort"`
	Speed       float64 `json:"speed"`

	Upload        bool   `json:"upload"`
	UploadLimit   int64  `json:"uploadLimit"`   // bytes per second, 0 = none
	DownloadLimit int64  `json:"downloadLimit"` // bytes per second, 0 = none
	Incoming      string `json:"incoming"`      // torrent peers can connect in: "reachable", "outgoing" (only), "" (not known yet)

	// Tunnels are paths of their own, listed after the interfaces.
	Tunnel      bool   `json:"tunnel,omitempty"`
	Of          string `json:"of,omitempty"`          // a tunnel's interface
	TunnelState string `json:"tunnelState,omitempty"` // "up", "connecting", "down" (retrying), "off" (its interface isn't used)
	TunnelError string `json:"tunnelError,omitempty"`
	HasTunnel   bool   `json:"hasTunnel,omitempty"` // an interface with a tunnel configured
	Provider    string `json:"provider,omitempty"`  // a tunnel's: "conf" or "pia"
}

type StateEvent struct {
	Downloads  []download.Summary `json:"downloads"`
	Interfaces []IfaceView        `json:"interfaces"`
	PIA        PIAView            `json:"pia"`
	Speed      float64            `json:"speed"`
	ETA        float64            `json:"eta"`
	Active     int                `json:"active"`
}

func (e *Engine) updateSnapshot(dt time.Duration) {
	sums := []download.Summary{}
	for _, d := range e.list() {
		sums = append(sums, d.Summary())
	}
	speeds := map[string]float64{}
	var total float64
	var remaining int64
	active := 0
	for _, s := range sums {
		for id, l := range s.Links {
			speeds[id] += l.Speed
		}
		if s.Status == download.StatusRunning {
			total += s.Speed
			active++
			if s.Size > 0 {
				remaining += s.Size - s.Done
			}
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	e.cum += total * dt.Seconds()
	e.meter.Window = time.Duration(e.settings.ETAWindowSec) * time.Second
	e.meter.Add(now, int64(e.cum))
	if active == 0 {
		e.meter.Reset()
		e.eta.Clear()
	} else {
		e.eta.Update(now, remaining, e.meter.Rate())
	}

	mainID := paths.MainInterface(e.pathSnapshotLocked())
	views := []IfaceView{}
	for id, ent := range e.ifaces {
		up, down := ent.shaper.Limits()
		views = append(views, IfaceView{
			ID: id, Name: ent.info.Name, Description: ent.info.Description, IP: ent.info.IPv4.String(),
			Present: ent.present, Enabled: e.enabledLocked(ent), Metered: ent.info.Metered, VPN: ent.info.VPN,
			IsDefault: ent.info.IsDefault, IsMain: id == mainID, Connections: e.connsLocked(id),
			SocksPort: ent.socks.Port(), Speed: speeds[id],
			Upload: e.uploadLocked(id), UploadLimit: up, DownloadLimit: down,
			Incoming: incomingText(ent.incoming), HasTunnel: e.tunnels[id] != nil,
		})
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Present != views[j].Present {
			return views[i].Present
		}
		return views[i].Name < views[j].Name
	})
	tviews := e.tunnelViewsLocked(speeds)
	sort.Slice(tviews, func(i, j int) bool { return tviews[i].Name < tviews[j].Name })
	views = append(views, tviews...)
	e.snapshot = StateEvent{Downloads: sums, Interfaces: views, PIA: e.piaViewLocked(), Speed: total, ETA: e.eta.Seconds(), Active: active}
}

func incomingText(in incoming) string {
	switch {
	case in.checked.IsZero():
		return ""
	case in.reachable:
		return "reachable"
	}
	return "outgoing"
}

func (e *Engine) Snapshot() StateEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snapshot
}
