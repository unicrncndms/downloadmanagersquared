// Package engine ties everything together: interfaces, downloads, the queue,
// persistence and the API that the extension (and later a GUI) talk to.
package engine

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"downloadmanagersquared/engine/internal/atomicfile"
	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/ipc"
	"downloadmanagersquared/engine/internal/metalink"
	"downloadmanagersquared/engine/internal/netif"
	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/portmap"
	"downloadmanagersquared/engine/internal/queue"
	"downloadmanagersquared/engine/internal/rate"
	"downloadmanagersquared/engine/internal/socks"
	"downloadmanagersquared/engine/internal/torrent"
	"downloadmanagersquared/engine/internal/update"
	"downloadmanagersquared/engine/internal/vpn"
	"downloadmanagersquared/engine/internal/winutil"
)

var ErrAlreadyRunning = errors.New("engine already running")

// Version is the release, set at build time from the git tag (build.ps1 -Version).
var Version = "dev"

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
	seq       int64  // higher = connected later
	browser   string // the exe of the browser the extension runs in, from its hello
	userAgent string // that browser's, from its hello
	// The landing-page hosts that browser has a recipe for, from recipeHosts;
	// nil until the extension says.
	recipeHosts map[string]bool
}

type resolveReq struct {
	ID        string `json:"id"`
	Iface     string `json:"iface"`
	IfaceName string `json:"ifaceName"`
	PageURL   string `json:"pageUrl"`
	SocksPort int    `json:"socksPort"`
	Learn     bool   `json:"learn,omitempty"` // only to record the recipe: no link is reported
	// CookieStore is the browser's cookie store the download came from: its
	// cookies for the landing page are copied into the resolve's container.
	CookieStore string `json:"cookieStore,omitempty"`
	Browser     string `json:"-"` // only the extension in this browser gets it ("" = any)
}

type Engine struct {
	dir string
	ln  net.Listener
	// One torrent session per VPN mode: a session's sockets are only the paths
	// its mode allows, so a VPN-only torrent has no bare socket to leak through.
	sessions map[paths.Mode]*torrent.Session
	queue    *queue.Queue

	mu        sync.Mutex
	settings  Settings
	ifaces    map[string]*ifaceEntry
	global    *rate.Shaper // the limits across every interface: each one's shaper is under it
	clients   map[*ipc.Conn]*client
	clientSeq int64
	resolves  map[string]resolveReq // waiting for an extension to pick them up
	learning  map[string]*ipc.Conn  // download id -> the extension recording its recipe (Record recipe)
	started   map[string]time.Time  // browsers started for a resolve, by exe, so not twice at once
	ipv6Only  []string              // adapters with only IPv6, left out while IPv6 is off: Settings hints at them
	userAgent string                // the browser's, from its latest hello; not saved
	browser   string                // the browser's exe, from its latest hello; not saved

	// Also under mu.
	window       Window // the Downloads window; nil until SetWindow
	windowFailed bool   // its Open has failed (logged once)

	// pickFolder shows Windows' folder picker (winutil.PickFolder); set once,
	// before the engine runs (tests replace it).
	pickFolder func(owner uintptr, start, title string) (string, bool)

	cum      float64 // bytes moved since start, integrated from speed (for the global ETA)
	meter    rate.Meter
	eta      rate.ETA
	snapshot StateEvent

	upd         update.State // the last check for a newer release; under mu
	updChecking bool         // a check is running; under mu

	mapPort func(ctx context.Context, gw portmap.Gateway, port int, lifetime time.Duration) error
	vpn     *vpn.Tunnels

	quit     chan struct{}
	quitOnce sync.Once
	quitReq  chan struct{} // closed when a client asks the engine to quit
	reqOnce  sync.Once
}

// New claims the pipe (proving this is the only engine of this user) and loads
// saved state. It is ErrAlreadyRunning only when another engine answers on the
// pipe; any other failure to open it is an error of its own.
func New() (*Engine, error) {
	ln, err := ipc.Listen()
	if err != nil {
		if c, derr := ipc.Dial(2 * time.Second); derr == nil {
			c.Close()
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("could not open the engine's pipe: %w", err)
	}
	dir, err := DataDir()
	if err != nil {
		ln.Close()
		return nil, err
	}
	e, err := open(dir, ln)
	if err != nil {
		ln.Close()
		return nil, err
	}
	e.refreshInterfaces()
	e.updateSnapshot(0)
	return e, nil
}

// DataDir is where the engine keeps the user's settings, state and log:
// %APPDATA%\downloadmanagersquared.
func DataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "downloadmanagersquared"), nil
}

// Open loads the engine kept in dir without claiming the pipe, so it runs
// beside an installed engine: its only clients are those of Dial. For checking
// the Downloads window by hand (cmd/dms-window-dev).
func Open(dir string) (*Engine, error) {
	e, err := open(dir, nil)
	if err != nil {
		return nil, err
	}
	e.refreshInterfaces()
	e.updateSnapshot(0)
	return e, nil
}

// Dial connects a client in this process, served the same as one on the pipe:
// the Downloads window's. Closing the returned end disconnects it.
func (e *Engine) Dial() net.Conn {
	ours, theirs := net.Pipe()
	go ipc.ServeConn(theirs, e)
	return ours
}

// open loads the engine kept in dir, with no interfaces yet. ln may be nil.
func open(dir string, ln net.Listener) (*Engine, error) {
	os.MkdirAll(filepath.Join(dir, "downloads"), 0o755)
	os.MkdirAll(filepath.Join(dir, "torrents"), 0o755)
	atomicfile.RemoveStale(dir)
	settings, broken := loadSettings(filepath.Join(dir, "settings.json"))
	oldPorts := legacyForwardedPorts(filepath.Join(dir, "settings.json")) // gone from the settings at the next save
	if broken {
		notify("Settings reset", "Settings couldn't be read and were reset; the old file is settings.json.broken.", nil)
	}
	e := &Engine{
		dir: dir,
		upd: update.Load(filepath.Join(dir, "update.json")),
		pickFolder: func(owner uintptr, start, title string) (string, bool) {
			return winutil.PickFolder(windows.Handle(owner), start, title)
		},
		ln:       ln,
		settings: settings,
		ifaces:   map[string]*ifaceEntry{},
		global:   rate.NewShaper(),
		clients:  map[*ipc.Conn]*client{},
		resolves: map[string]resolveReq{},
		learning: map[string]*ipc.Conn{},
		started:  map[string]time.Time{},
		mapPort:  portmap.Map,
		quit:     make(chan struct{}),
		quitReq:  make(chan struct{}),
	}
	e.saveSettingsLocked() // keeps the torrent port picked on the first start
	e.sessions = map[paths.Mode]*torrent.Session{}
	// Alongside listens nowhere: the other two hold the torrent port on the
	// interfaces and in the tunnels.
	ports := map[paths.Mode]int{paths.Off: e.settings.TorrentPort, paths.VPNOnly: e.settings.TorrentPort}
	for _, m := range []paths.Mode{paths.Off, paths.Alongside, paths.VPNOnly} {
		s, err := torrent.NewSession(torrent.Config{
			Changed: func(t *torrent.Torrent) { e.queue.MarkDirty(t.ID()) },
			Finished: func(t *torrent.Torrent) {
				markTorrent(t.ID(), t.Path())
				e.notifyFinished(t.Summary(), t.Path())
			},
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
	e.loadVPN()
	for id, port := range oldPorts {
		e.vpn.AdoptForwardedPort(id, port)
	}
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

// loadDownloads restores both kinds, in the order they were added. Unreadable
// state files are set aside, with one balloon for all of them.
func (e *Engine) loadDownloads() {
	var all []queue.Entry
	states, broken := readStates[download.State](filepath.Join(e.dir, "downloads"))
	for _, st := range states {
		if st.Status == download.StatusRunning || st.Status == download.StatusVerifying {
			st.Status = download.StatusQueued
		}
		st.VPN = paths.ParseMode(string(st.VPN), paths.Off) // saved before modes were spelled out
		all = append(all, download.New(e, st))
	}
	tstates, tbroken := readStates[torrent.State](filepath.Join(e.dir, "torrents"))
	if n := broken + tbroken; n == 1 {
		notify("A download couldn't be restored", "Its unfinished file ends in .dms.part and can be deleted.", nil)
	} else if n > 1 {
		notify("Downloads couldn't be restored", fmt.Sprintf("%d downloads couldn't be restored. Their unfinished files end in .dms.part and can be deleted.", n), nil)
	}
	for _, st := range tstates {
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
		all = append(all, t)
	}
	e.queue = queue.New(e.dir, all)
	for _, st := range states {
		if st.HasPlainCookies() {
			e.queue.MarkDirty(st.ID) // saved encrypted at the next tick, not left readable
		}
	}
}

// readStates reads every saved state in dir that has an ID. The others are
// set aside as <id>.json.broken; broken counts them.
func readStates[S interface{ download.State | torrent.State }](dir string) (out []S, broken int) {
	older, _ := filepath.Glob(filepath.Join(dir, "*.json.broken"))
	for _, f := range older { // set aside by a version that kept their cookies
		if b, err := os.ReadFile(f); err == nil {
			if clean := withoutCookies(b); !bytes.Equal(clean, b) {
				if err := atomicfile.Write(f, clean, 0o600); err != nil {
					log.Printf("removing cookies from %s: %v", f, err)
				}
			}
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		var st S
		var id struct {
			ID string `json:"id"`
		}
		b, err := os.ReadFile(f)
		if err != nil || json.Unmarshal(b, &st) != nil || json.Unmarshal(b, &id) != nil || id.ID == "" {
			log.Printf("unreadable state %s, set aside", f)
			setAsideWithoutCookies(f, b)
			broken++
			continue
		}
		out = append(out, st)
	}
	return out, broken
}

// Run serves the API and drives the queue until Shutdown.
func (e *Engine) Run() {
	if e.ln != nil {
		go ipc.Serve(e.ln, e)
	}
	go e.mapPorts()
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
			e.tickVPN(now)
			e.checkUpdate(now)
			e.syncTorrentIfaces()
			e.schedule()
			e.queue.Save()
			e.updateSnapshot(now.Sub(last))
			last = now
			e.broadcast("state", e.Snapshot())
		}
	}
}

// QuitRequested is closed when a client (an upgrade or the uninstaller) asks
// the engine to quit; the tray then quits as if from its menu.
func (e *Engine) QuitRequested() <-chan struct{} { return e.quitReq }

// Shutdown saves everything, then lets go of the pipe: whoever waits for the
// pipe to be free finds the state on disk.
func (e *Engine) Shutdown() {
	e.quitOnce.Do(func() {
		close(e.quit)
		e.queue.Close()
		if e.ln != nil {
			e.ln.Close()
		}
		for _, s := range e.sessions {
			s.Close()
		}
		e.vpn.Close()
		e.mu.Lock()
		for _, ent := range e.ifaces {
			ent.socks.Close()
		}
		e.mu.Unlock()
	})
}

// get is for what only HTTP downloads have: links, hashes, landing pages.
func (e *Engine) get(id string) (*download.Download, error) {
	j, err := e.queue.Get(id)
	if err != nil {
		return nil, err
	}
	d, ok := j.(*download.Download)
	if !ok {
		return nil, fmt.Errorf("%q is not an HTTP download", id)
	}
	return d, nil
}

// schedule lets the queue start what it can, under the current limit and paths.
func (e *Engine) schedule() {
	e.mu.Lock()
	limit, snap := e.settings.MaxParallel, e.pathSnapshotLocked()
	e.mu.Unlock()
	e.queue.Schedule(limit, snap)
}

func (e *Engine) saveSettingsLocked() {
	if err := atomicfile.WriteJSON(filepath.Join(e.dir, "settings.json"), e.settings); err != nil {
		log.Printf("saving settings: %v", err)
	}
}

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

// dialVia dials through a path: an interface, or its tunnel.
func (e *Engine) dialVia(id string) socks.DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if ifaceID, ok := paths.IfaceOf(id); ok {
			tn, ok := e.vpn.Up(ifaceID)
			if !ok {
				return nil, errors.New("the VPN tunnel is not up")
			}
			return tn.Dial(ctx, network, addr)
		}
		b, err := e.bindingOf(id)
		if err != nil {
			return nil, err
		}
		return b.DialContext(ctx, network, addr)
	}
}

// socksPath is a path as a resolve's SOCKS proxy sends its connections out.
func (e *Engine) socksPath(id string) socks.Path {
	return socks.Path{Dial: e.dialVia(id), Lookup: func(ctx context.Context, host string) ([]net.IP, error) {
		if ifaceID, ok := paths.IfaceOf(id); ok {
			tn, ok := e.vpn.Up(ifaceID)
			if !ok {
				return nil, errors.New("the VPN tunnel is not up")
			}
			return tn.LookupIP(ctx, host)
		}
		b, err := e.bindingOf(id)
		if err != nil {
			return nil, err
		}
		return b.LookupIP(ctx, host)
	}, IPv6: func() bool { // a tunnel carries only IPv4
		if _, tun := paths.IfaceOf(id); tun {
			return false
		}
		b, err := e.bindingOf(id)
		return err == nil && b.Has6()
	}}
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
			tn, ok := e.vpn.Up(base)
			if !ok { // went down since the snapshot
				continue
			}
			ifs = append(ifs, torrent.Iface{
				ID: id, Gen: tn.Gen, Port: tn.Port, Upload: e.uploadLocked(base),
				Dial: tn.Dial, ListenPacket: tn.ListenPacket, LookupIP: tn.LookupIP, Listen: tn.Listen,
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
	for _, id := range paths.For(m, e.pathSnapshotLocked()).Paths {
		base, tun := paths.IfaceOf(id)
		var client *http.Client
		var byFamily map[string]*http.Client
		if tun {
			tn, ok := e.vpn.Up(base)
			if !ok { // went down since the snapshot
				continue
			}
			client = tn.Client
		} else {
			b := e.ifaces[id].binding
			client, byFamily = b.HTTPClient(), b.FamilyClients()
		}
		out = append(out, download.Iface{ID: id, Client: client, ByFamily: byFamily, Conns: e.connsLocked(base)})
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

func (e *Engine) ETAWindow() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return time.Duration(e.settings.ETAWindowSec) * time.Second
}

// RequestResolve has the extension in the browser the download came from open
// its page through ifaceID: the page's clicks are learned in that browser, and
// its cookies are there. If that browser isn't running, it is started and
// gets the request once its extension connects.
func (e *Engine) RequestResolve(id, ifaceID, pageURL, browser string) {
	if browser != "" {
		if fi, err := os.Stat(browser); err != nil || fi.IsDir() {
			browser = "" // uninstalled since: any browser will do
		}
	}
	store := ""
	if d, err := e.get(id); err == nil {
		store = d.CookieStore()
	}
	e.mu.Lock()
	req := e.resolveReqLocked(id, ifaceID, pageURL, browser)
	req.CookieStore = store
	e.resolves[id+"|"+ifaceID] = req
	c := e.extensionForLocked(browser)
	start := c == nil && browser != "" && time.Since(e.started[strings.ToLower(browser)]) > time.Minute
	if start {
		e.started[strings.ToLower(browser)] = time.Now()
	}
	e.mu.Unlock()
	log.Printf("resolve %s via %s", id, req.IfaceName)
	switch {
	case c != nil:
		c.Emit("resolve", req)
	case start:
		if err := startBrowser(browser, ""); err != nil {
			log.Printf("starting the browser for a resolve: %v", err)
		}
	}
}

func (e *Engine) resolveReqLocked(id, ifaceID, pageURL, browser string) resolveReq {
	req := resolveReq{ID: id, Iface: ifaceID, PageURL: pageURL, Browser: browser}
	if base, tun := paths.IfaceOf(ifaceID); tun {
		if st, ok := e.tunnelStatus(base); ok && e.ifaces[base] != nil {
			req.IfaceName, req.SocksPort = "VPN via "+e.ifaces[base].info.Name, st.SocksPort
		}
	} else if ent := e.ifaces[ifaceID]; ent != nil {
		req.IfaceName, req.SocksPort = ent.info.Name, ent.socks.Port()
	}
	return req
}

// RecordRecipe has the browser a download came from record its landing
// page's recipe now, through the main interface, while the download keeps its
// link: a resolve marked learn, outside needResolve, so no link changes.
func (e *Engine) RecordRecipe(id string) error {
	d, err := e.get(id)
	if err != nil {
		return err
	}
	st := d.State()
	if st.PageURL == "" {
		return fmt.Errorf("%s has no landing page", st.Filename)
	}
	e.mu.Lock()
	c := e.extensionForLocked(st.Browser)
	if c == nil {
		e.mu.Unlock()
		return errors.New("the browser this download came from isn't running")
	}
	req := e.resolveReqLocked(id, paths.MainInterface(e.pathSnapshotLocked()), st.PageURL, st.Browser)
	req.CookieStore = st.CookieStore
	req.Learn = true
	e.learning[id] = c
	e.mu.Unlock()
	log.Printf("recording a recipe for %s via %s", id, req.IfaceName)
	c.Emit("resolve", req)
	return nil
}

// canRecordLocked: the Record recipe button. An unfinished HTTP download with a
// landing page whose browser's extension is connected, has said which hosts
// it has recipes for, and has none for this one; with no resolve or
// recording running for it.
func (e *Engine) canRecordLocked(s download.Summary, browser string) bool {
	if s.Kind != "" || s.Status == download.StatusCompleted || s.PageURL == "" {
		return false
	}
	if c := e.learning[s.ID]; c != nil && e.clients[c] != nil {
		return false
	}
	for k := range e.resolves {
		if strings.HasPrefix(k, s.ID+"|") {
			return false
		}
	}
	for _, l := range s.Links {
		if l.Status == download.LinkResolving {
			return false
		}
	}
	c := e.extensionForLocked(browser)
	if c == nil {
		return false
	}
	hosts := e.clients[c].recipeHosts
	u, err := url.Parse(s.PageURL)
	return hosts != nil && err == nil && u.Hostname() != "" && !hosts[u.Hostname()]
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

// startBrowser starts a browser so its extension connects; tests replace it.
var startBrowser = winutil.OpenBrowser

func (e *Engine) Changed(d *download.Download) { e.queue.MarkDirty(d.ID()) }

func (e *Engine) Finished(d *download.Download) { e.notifyFinished(d.Summary(), d.Path()) }

// notify shows a tray balloon, running click when it is clicked; tests switch it off.
var notify = winutil.NotifyClick

// notifyFinished says a download ended; a click on a finished one shows it in its folder.
// markTorrent writes the Mark of the Web on every file under path, the
// torrent's file or folder. No URL: a torrent's files come from peers.
func markTorrent(id, path string) {
	if path == "" {
		return
	}
	filepath.WalkDir(path, func(p string, de fs.DirEntry, err error) error {
		if err == nil && de.Type().IsRegular() {
			if err := winutil.MarkFromInternet(p, "", ""); err != nil {
				log.Printf("torrent %s: marking %s as from the internet: %v", id, filepath.Base(p), err)
				return filepath.SkipAll // the volume takes no streams: the rest won't either
			}
		}
		return nil
	})
}

func (e *Engine) notifyFinished(s download.Summary, path string) {
	if s.Status == download.StatusCompleted || s.Status == torrent.StatusSeeding {
		msg := s.Filename
		if f := s.Folder; f != nil && f.On && f.Out != "" {
			msg = "Extracted to " + filepath.Base(f.Out)
		}
		notify("Download complete", msg, func() { winutil.ShowInFolder(path) })
	} else {
		notify("Download failed", s.Filename+": "+s.Error, nil)
	}
}

// extensionLocked is the most recently connected browser extension. Only
// OpenManager's fallback (WebView2 missing) still needs it.
func (e *Engine) extensionLocked() *ipc.Conn { return e.extensionForLocked("") }

// extensionForLocked is the most recently connected extension in the browser
// at exe ("" = in any browser).
func (e *Engine) extensionForLocked(exe string) *ipc.Conn {
	var best *ipc.Conn
	var seq int64 = -1
	for c, cl := range e.clients {
		if cl.extension && cl.seq > seq && (exe == "" || strings.EqualFold(cl.browser, exe)) {
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

// Window is the Downloads window (internal/window), or a fake in tests.
type Window interface {
	// Open shows view: "" the downloads, "#settings" (or another "#hash") the
	// settings page. It fails only if the window can't be created.
	Open(view string) error
}

// windowHandle is a Downloads window that can say which window it is, so a
// dialog opens over it: the real one (Handle is 0 while it is closed).
type windowHandle interface{ Handle() uintptr }

// SetWindow gives the engine the Downloads window, for OpenManager.
func (e *Engine) SetWindow(w Window) {
	e.mu.Lock()
	e.window = w
	e.mu.Unlock()
}

// OpenManager shows view ("" the downloads, "#settings" or another "#hash" the
// settings page) in the Downloads window. Only if the window can't be created
// (WebView2 missing) does it fall back to the browser the extension last ran in.
func (e *Engine) OpenManager(view string) {
	e.mu.Lock()
	w := e.window
	e.mu.Unlock()
	if w == nil {
		e.openInBrowser(view)
		return
	}
	err := w.Open(view)
	if err == nil {
		return
	}
	e.mu.Lock()
	first := !e.windowFailed
	e.windowFailed = true
	e.mu.Unlock()
	if first {
		log.Printf("the Downloads window: %v; showing it in the browser instead", err)
	}
	e.openInBrowser(view)
}

// openInBrowser is OpenManager's fallback, the only path left that asks a
// browser to show the UI: a connected extension is told to show view;
// otherwise the browser the extension last ran in is started (its toolbar
// button then shows the downloads).
func (e *Engine) openInBrowser(view string) {
	e.mu.Lock()
	c := e.extensionLocked()
	browser := e.browser
	e.mu.Unlock()
	if c != nil {
		c.Emit("openManager", map[string]string{"hash": view})
		return
	}
	if err := startBrowser(browser, ""); err != nil {
		log.Printf("starting the browser: %v", err)
	}
}

func (e *Engine) PauseAll()  { e.queue.PauseAll() }
func (e *Engine) ResumeAll() { e.queue.ResumeAll() }

// setVPNAll gives every unfinished download (or, with torrents, every
// torrent) VPN mode m. An HTTP download switches paths within a tick. A
// torrent leaves the swarm and joins again through m's session; one still
// asked about the VPN, or whose files are being chosen, keeps waiting for the user.
func (e *Engine) setVPNAll(torrents bool, m paths.Mode) {
	for _, j := range e.queue.List() {
		var err error
		switch j := j.(type) {
		case *download.Download:
			if !torrents && j.Status() != download.StatusCompleted {
				err = j.SetVPN(m)
			}
		case *torrent.Torrent:
			st := j.State()
			if torrents && !st.AskVPN && st.Status != torrent.StatusChoosing && st.Status != download.StatusCompleted {
				err = e.moveTorrent(j, m)
			}
		}
		if err != nil {
			log.Printf("VPN mode for %s: %v", j.ID(), err)
		}
	}
	e.schedule() // pause them for a tunnel, or start them again, now
}

// moveTorrent puts a torrent in the session of VPN mode m, keeping its state
// and its place in the queue.
func (e *Engine) moveTorrent(t *torrent.Torrent, m paths.Mode) error {
	st := t.State()
	if st.VPN == m {
		return nil
	}
	if t.Active() {
		as := download.StatusQueued // back in line, keeping its place
		if !st.Finished.IsZero() {
			as = torrent.StatusSeeding
		}
		t.Stop(as)
		st = t.State()
	}
	st.VPN, st.WaitVPN = m, false
	nt, err := e.session(m).Add(st)
	if err != nil {
		return err
	}
	return e.queue.Replace(t, nt)
}

// chooseTorrentVPN answers a new torrent's VPN question, moving it to the
// session of that mode before it joins the swarm.
func (e *Engine) chooseTorrentVPN(id, vpn string) (*torrent.Torrent, error) {
	j, err := e.queue.Get(id)
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
	if err := e.queue.Replace(t, nt); err != nil {
		return nil, err
	}
	nt.Start() // learn the files through the chosen paths, or back to choosing them
	return nt, nil
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
	Torrent   bool   `json:"torrent"`  // the URL serves a .torrent file
	Metalink  bool   `json:"metalink"` // the URL serves a Metalink (.meta4)
	VPN       string `json:"vpn"`      // "off", "alongside" or "vpn"; "" = the default for its type
	Folder    *bool  `json:"folder"`   // Download as folder: on (asks for the folder), off, or nil for the default
	Browser   string `json:"-"`        // the exe of the browser that sent it, known from its connection
	// CookieStore is the browser's cookie store (container) the download came from.
	CookieStore string `json:"cookieStore"`
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
		// Some servers drop clients with Go's default user agent: use the browser's.
		e.mu.Lock()
		r.UserAgent = e.userAgent
		e.mu.Unlock()
	}
	if r.UserAgent == "" {
		r.UserAgent = defaultUserAgent
	}
	if r.Torrent || isTorrentURL(r.URL) {
		return e.addTorrentURL(r)
	}
	if r.Metalink || isMetalinkURL(r.URL) {
		return e.addMetalinkURL(r)
	}
	e.mu.Lock()
	st := download.State{
		VPN: paths.ParseMode(r.VPN, e.settings.VPNHTTP),
		ID:  newID(), URL: r.URL, PageURL: strings.TrimSpace(r.PageURL),
		Cookie: download.Secret(r.Cookie), Referer: r.Referer, UserAgent: r.UserAgent, Browser: r.Browser,
		CookieStore: r.CookieStore, Dir: e.settings.SaveDir, Filename: download.SanitizeFilename(r.Filename),
		Size: -1, Status: download.StatusQueued, Added: time.Now(),
	}
	switch {
	case r.Folder != nil && *r.Folder:
		// Switched on by hand: it asks where to extract (settled once the name
		// is known: only a .zip stays one).
		st.Folder = &download.Folder{On: true}
	case r.Folder == nil && e.settings.FolderDefault:
		// Settled once the name is known: only a .zip becomes one.
		st.Folder = &download.Folder{Auto: true, Into: e.settings.folderDir()}
	}
	e.mu.Unlock()
	e.queue.Add(download.New(e, st))
	log.Printf("added %s", st.ID) // no URL: links are private
	return st.ID, nil
}

// PickFolder shows the folder picker for a download's folder to extract into,
// over the Downloads window when it is open, and sets the folder chosen; once
// one is set, choosing another moves what is extracted there. It
// returns at once: the picker can stay open longer than a request may take,
// and the extension's popup closes as soon as it opens. The choice shows up
// in the download's state.
func (e *Engine) PickFolder(id string) error {
	d, err := e.get(id)
	if err != nil {
		return err
	}
	st := d.State()
	f := st.Folder
	if f == nil || !f.On || st.Status == download.StatusCompleted {
		return errors.New("this download isn't being extracted as a folder")
	}
	e.mu.Lock()
	start := e.settings.folderDir()
	if f.Into != "" {
		start = f.Into // a move: from where it is
	}
	var owner uintptr
	if h, ok := e.window.(windowHandle); ok {
		owner = h.Handle()
	}
	pick := e.pickFolder
	e.mu.Unlock()
	name := st.Filename
	if name == "" {
		name = "the archive"
	}
	go func() {
		title := "Choose where to extract " + name
		if f.Into != "" {
			title = "Choose where to move the files extracted from " + name
		}
		dir, ok := pick(owner, start, title)
		if !ok {
			return
		}
		if err := e.SetFolder(id, nil, &dir); err != nil {
			log.Printf("download %s: the folder chosen: %v", id, err)
		}
	}()
	return nil
}

// SetFolder sets a download's folder to extract into, which becomes the last
// one used, and switches Download as folder on or off; either may be nil.
func (e *Engine) SetFolder(id string, on *bool, into *string) error {
	d, err := e.get(id)
	if err != nil {
		return err
	}
	// The switch first: refused, it leaves the folder and the setting as they were.
	if on != nil {
		if err := d.SetFolder(*on); err != nil {
			return err
		}
	}
	if into != nil {
		dir := strings.TrimSpace(*into)
		if err := d.SetFolderInto(dir); err != nil {
			return err
		}
		e.applySettings(func(s *Settings) { s.useFolder(filepath.Clean(dir)) })
	}
	return nil
}

func isTorrentURL(u string) bool {
	p, err := url.Parse(u)
	return err == nil && strings.HasSuffix(strings.ToLower(p.Path), ".torrent")
}

func isMetalinkURL(u string) bool {
	p, err := url.Parse(u)
	return err == nil && strings.HasSuffix(strings.ToLower(p.Path), ".meta4")
}

const maxSmallFile = 16 << 20

// fetchSmallFile downloads a .torrent or .meta4 file (what) the way the browser would have.
func fetchSmallFile(ctx context.Context, c *http.Client, r AddRequest, what string) ([]byte, error) {
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
		return nil, fmt.Errorf("the %s's server answered %s", what, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxSmallFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSmallFile {
		return nil, errors.New("too big for a " + what)
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
	b, err := fetchSmallFile(context.Background(), ps[0].Client, r, "torrent file")
	if err != nil {
		return "", err
	}
	return e.addTorrentFile(b, r.VPN)
}

// addMetalinkURL fetches a Metalink through the main interface and adds a
// download for each file in it, with its mirrors and checksums. Several files
// share a group. The ID returned is the first file's.
func (e *Engine) addMetalinkURL(r AddRequest) (string, error) {
	e.mu.Lock()
	m := paths.ParseMode(r.VPN, e.settings.VPNHTTP)
	e.mu.Unlock()
	ps := e.Paths(m)
	if len(ps) == 0 {
		return "", errors.New("no connection to fetch the Metalink through")
	}
	b, err := fetchSmallFile(context.Background(), ps[0].Client, r, "Metalink")
	if err != nil {
		return "", err
	}
	return e.addMetalink(b, r, m)
}

func (e *Engine) addMetalink(b []byte, r AddRequest, m paths.Mode) (string, error) {
	files, err := metalink.Parse(b)
	if err != nil {
		return "", err
	}
	group, name := "", ""
	if len(files) > 1 {
		group = newID()
		name = download.SanitizeFilename(r.Filename)
		if p, err := url.Parse(r.URL); err == nil && name == "" {
			name = download.SanitizeFilename(path.Base(p.Path))
		}
		name = strings.TrimSuffix(cmp.Or(name, "Metalink"), ".meta4")
	}
	e.mu.Lock()
	var ids []string
	var ds []*download.Download
	for _, f := range files {
		st := download.State{
			VPN: m, ID: newID(), URL: f.URLs[0], UserAgent: r.UserAgent, Browser: r.Browser,
			Dir: e.settings.SaveDir, Filename: download.SanitizeFilename(path.Base(f.Name)),
			Size: -1, MetaSize: f.Size, SHA256: f.SHA256, MD5: f.MD5,
			Status: download.StatusQueued, Added: time.Now(), Group: group, GroupName: name,
		}
		// No cookie or referer: they were the Metalink site's, not the mirrors'.
		for i, u := range f.URLs {
			if i < download.MaxMirrors {
				st.Mirrors = append(st.Mirrors, download.Mirror{URL: u})
			}
		}
		if st.SHA256 == "" && st.MD5 == "" && e.settings.FolderDefault {
			st.Folder = &download.Folder{Auto: true, Into: e.settings.folderDir()}
		}
		ids = append(ids, st.ID)
		ds = append(ds, download.New(e, st))
	}
	e.mu.Unlock()
	for _, d := range ds {
		e.queue.Add(d)
	}
	log.Printf("added Metalink: %d downloads", len(ids))
	return ids[0], nil
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
	e.queue.Add(t)
	log.Printf("added torrent %s", t.ID())
	return t.ID(), nil
}

func (e *Engine) Remove(id string, deleteFile bool) error {
	if err := e.queue.Remove(id, deleteFile); err != nil {
		return err
	}
	e.mu.Lock()
	for k := range e.resolves {
		if strings.HasPrefix(k, id+"|") {
			delete(e.resolves, k)
		}
	}
	e.mu.Unlock()
	return nil
}

type IfaceView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	IP          string   `json:"ip"` // IPv4, else IPv6
	IP6         string   `json:"ip6,omitempty"`
	Families    []string `json:"families,omitempty"` // "IPv4", "IPv6": the badge
	Present     bool     `json:"present"`
	Enabled     bool     `json:"enabled"`
	VPN         bool     `json:"vpn"`
	IsDefault   bool     `json:"isDefault"`
	IsMain      bool     `json:"isMain"`
	Connections int      `json:"connections"`
	SocksPort   int      `json:"socksPort"`
	Speed       float64  `json:"speed"`

	Upload        bool   `json:"upload"`
	UploadLimit   int64  `json:"uploadLimit"`   // bytes per second, 0 = none
	DownloadLimit int64  `json:"downloadLimit"` // bytes per second, 0 = none
	Incoming      string `json:"incoming"`      // torrent peers can connect in: "reachable", "forwarded" (a port the user typed in, unchecked), "outgoing" (only), "" (not known yet)

	// Tunnels are paths of their own, listed after the interfaces.
	Tunnel        bool   `json:"tunnel,omitempty"`
	Of            string `json:"of,omitempty"`          // a tunnel's interface
	TunnelState   string `json:"tunnelState,omitempty"` // "up", "connecting", "down" (retrying), "off" (its interface isn't used)
	TunnelError   string `json:"tunnelError,omitempty"`
	HasTunnel     bool   `json:"hasTunnel,omitempty"`     // an interface with a tunnel configured
	Provider      string `json:"provider,omitempty"`      // a tunnel's: "conf" or "pia"
	ForwardedPort int    `json:"forwardedPort,omitempty"` // an imported tunnel's port typed in by the user
	// The other enabled interfaces behind the same router (same gateway MAC),
	// by name: together they are no faster than one. Only a warning.
	SameRouter []string `json:"sameRouter,omitempty"`
}

type StateEvent struct {
	Downloads  []download.Summary `json:"downloads"`
	Interfaces []IfaceView        `json:"interfaces"`
	PIA        vpn.PIAView        `json:"pia"`
	Speed      float64            `json:"speed"`
	ETA        float64            `json:"eta"`
	Active     int                `json:"active"`
	Update     *UpdateView        `json:"update,omitempty"`   // a newer release exists
	IPv6Only   []string           `json:"ipv6Only,omitempty"` // adapters left out because they have only IPv6 and IPv6 is off
}

func (e *Engine) updateSnapshot(dt time.Duration) {
	sums := []download.Summary{}
	browsers := []string{} // each download's browser, for Record recipe
	for _, d := range e.queue.List() {
		sums = append(sums, d.Summary())
		b := ""
		if hd, ok := d.(*download.Download); ok {
			b = hd.Browser()
		}
		browsers = append(browsers, b)
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
	for i := range sums {
		sums[i].Actions.RecordRecipe = e.canRecordLocked(sums[i], browsers[i])
	}
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
	tunnels := e.vpn.Status(now)
	hasTunnel := map[string]bool{}
	for _, st := range tunnels {
		hasTunnel[st.Iface] = true
	}
	var used []netif.Interface
	for _, ent := range e.ifaces {
		if ent.present && e.enabledLocked(ent) {
			used = append(used, ent.info)
		}
	}
	sameRouter := netif.SameRouter(used)
	views := []IfaceView{}
	for id, ent := range e.ifaces {
		var same []string
		for _, other := range sameRouter[id] {
			same = append(same, e.ifaces[other].info.Name)
		}
		sort.Strings(same)
		up, down := ent.shaper.Limits()
		views = append(views, IfaceView{
			ID: id, Name: ent.info.Name, Description: ent.info.Description, IP: ipText(ent.info.IP()), IP6: ipText(ent.info.IPv6),
			Families: ent.info.Families(),
			Present:  ent.present, Enabled: e.enabledLocked(ent), VPN: ent.info.VPN,
			IsDefault: ent.info.IsDefault, IsMain: id == mainID, Connections: e.connsLocked(id),
			SocksPort: ent.socks.Port(), Speed: speeds[id],
			Upload: e.uploadLocked(id), UploadLimit: up, DownloadLimit: down,
			Incoming: incomingText(ent.incoming), HasTunnel: hasTunnel[id], SameRouter: same,
		})
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Present != views[j].Present {
			return views[i].Present
		}
		return views[i].Name < views[j].Name
	})
	tviews := e.tunnelViewsLocked(tunnels, speeds)
	sort.Slice(tviews, func(i, j int) bool { return tviews[i].Name < tviews[j].Name })
	views = append(views, tviews...)
	e.snapshot = StateEvent{Downloads: sums, Interfaces: views, PIA: e.vpn.PIA(), Speed: total, ETA: e.eta.Seconds(), Active: active, Update: e.updateViewLocked(), IPv6Only: e.ipv6Only}
}

func ipText(ip net.IP) string {
	if ip == nil {
		return ""
	}
	return ip.String()
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
