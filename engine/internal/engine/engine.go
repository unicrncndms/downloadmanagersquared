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
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/ipc"
	"downloadmanagersquared/engine/internal/netif"
	"downloadmanagersquared/engine/internal/rate"
	"downloadmanagersquared/engine/internal/socks"
	"downloadmanagersquared/engine/internal/winutil"
)

var ErrAlreadyRunning = errors.New("engine already running")

const Version = "0.1.0"

const defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0"

type ifaceEntry struct {
	info    netif.Interface
	binding *netif.Binding
	socks   *socks.Server
	present bool
}

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

type Engine struct {
	dir string
	ln  net.Listener

	mu        sync.Mutex
	settings  Settings
	ifaces    map[string]*ifaceEntry
	downloads map[string]*download.Download
	order     []string
	dirty     map[string]bool
	clients   map[*ipc.Conn]*client
	clientSeq int64
	resolves  map[string]resolveReq // waiting for an extension to pick them up
	openWant  *string               // view to open once Firefox connects ("#settings" or downloads)

	cum      float64 // bytes moved since start, integrated from speed (for the global ETA)
	meter    rate.Meter
	eta      float64
	etaAt    time.Time
	snapshot StateEvent

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
	dir := filepath.Join(base, "downloadmanagersquared")
	os.MkdirAll(filepath.Join(dir, "downloads"), 0o755)

	e := &Engine{
		dir:       dir,
		ln:        ln,
		settings:  loadSettings(filepath.Join(dir, "settings.json")),
		ifaces:    map[string]*ifaceEntry{},
		downloads: map[string]*download.Download{},
		dirty:     map[string]bool{},
		clients:   map[*ipc.Conn]*client{},
		resolves:  map[string]resolveReq{},
		eta:       -1,
		quit:      make(chan struct{}),
	}
	e.meter.Window = time.Duration(e.settings.ETAWindowSec) * time.Second
	e.loadDownloads()
	e.refreshInterfaces()
	e.updateSnapshot(0)
	return e, nil
}

func (e *Engine) Dir() string { return e.dir }

func (e *Engine) loadDownloads() {
	files, _ := filepath.Glob(filepath.Join(e.dir, "downloads", "*.json"))
	var states []download.State
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var st download.State
		if json.Unmarshal(b, &st) != nil || st.ID == "" {
			log.Printf("skipping unreadable download state %s", f)
			continue
		}
		if st.Status == download.StatusRunning || st.Status == download.StatusVerifying {
			st.Status = download.StatusQueued
		}
		states = append(states, st)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Added.Before(states[j].Added) })
	for _, st := range states {
		e.downloads[st.ID] = download.New(e, st)
		e.order = append(e.order, st.ID)
	}
}

// Run serves the API and drives the queue until Shutdown.
func (e *Engine) Run() {
	go ipc.Serve(e.ln, e)
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
		e.ln.Close()
		for _, d := range e.list() {
			if d.Running() {
				d.Stop(download.StatusQueued)
			}
			e.Changed(d)
		}
		e.persist()
		e.mu.Lock()
		for _, ent := range e.ifaces {
			ent.socks.Close()
		}
		e.mu.Unlock()
	})
}

func (e *Engine) list() []*download.Download {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*download.Download, 0, len(e.order))
	for _, id := range e.order {
		out = append(out, e.downloads[id])
	}
	return out
}

func (e *Engine) get(id string) (*download.Download, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.downloads[id]
	if d == nil {
		return nil, fmt.Errorf("no download %q", id)
	}
	return d, nil
}

// schedule starts queued downloads while there are free slots.
func (e *Engine) schedule() {
	e.mu.Lock()
	limit := e.settings.MaxParallel
	e.mu.Unlock()
	all := e.list()
	running := 0
	for _, d := range all {
		if d.Running() {
			running++
		}
	}
	for _, d := range all {
		if running >= limit {
			return
		}
		if !d.Running() && d.Status() == download.StatusQueued {
			d.Start()
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
		d, err := e.get(id)
		if err != nil {
			continue
		}
		if err := writeJSON(e.statePath(id), d.State()); err != nil {
			log.Printf("saving %s: %v", id, err)
		}
	}
}

func (e *Engine) statePath(id string) string { return filepath.Join(e.dir, "downloads", id+".json") }

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
	defer e.mu.Unlock()
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
			ent = &ifaceEntry{socks: srv}
			e.ifaces[i.ID] = ent
			log.Printf("interface %s (%s) %s, socks port %d", i.Name, i.Description, i.IPv4, srv.Port())
		}
		if ent.binding == nil || !ent.info.IPv4.Equal(i.IPv4) || ent.info.Index != i.Index || fmt.Sprint(ent.info.DNS) != fmt.Sprint(i.DNS) {
			if ent.binding != nil {
				ent.binding.Close()
			}
			ent.binding = netif.NewBinding(i)
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
}

func (e *Engine) dialVia(id string) socks.DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		e.mu.Lock()
		ent := e.ifaces[id]
		var b *netif.Binding
		if ent != nil && ent.present {
			b = ent.binding
		}
		e.mu.Unlock()
		if b == nil {
			return nil, errors.New("interface is not connected")
		}
		return b.DialContext(ctx, network, addr)
	}
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

func (e *Engine) Usable() []download.Iface {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []download.Iface
	for id, ent := range e.ifaces {
		if ent.present && e.enabledLocked(ent) {
			out = append(out, download.Iface{ID: id, Client: ent.binding.HTTPClient(), Conns: e.connsLocked(id)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (e *Engine) MainID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.mainIDLocked()
}

// mainIDLocked: the chosen main connection, else the Windows default route, else any usable one.
func (e *Engine) mainIDLocked() string {
	if ent := e.ifaces[e.settings.MainInterface]; ent != nil && ent.present && e.enabledLocked(ent) {
		return e.settings.MainInterface
	}
	var fallback string
	for id, ent := range e.ifaces {
		if !ent.present || !e.enabledLocked(ent) {
			continue
		}
		if ent.info.IsDefault {
			return id
		}
		if fallback == "" || id < fallback {
			fallback = id
		}
	}
	return fallback
}

func (e *Engine) ETAWindow() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return time.Duration(e.settings.ETAWindowSec) * time.Second
}

func (e *Engine) RequestResolve(id, ifaceID, pageURL string) {
	e.mu.Lock()
	req := resolveReq{ID: id, Iface: ifaceID, PageURL: pageURL}
	if ent := e.ifaces[ifaceID]; ent != nil {
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

func (e *Engine) Changed(d *download.Download) {
	e.mu.Lock()
	e.dirty[d.ID()] = true
	e.mu.Unlock()
}

func (e *Engine) Finished(d *download.Download) {
	s := d.Summary()
	if s.Status == download.StatusCompleted {
		winutil.Notify("Download complete", s.Filename)
	} else {
		winutil.Notify("Download failed", s.Filename+": "+s.Error)
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
	for _, c := range cs {
		c.Emit(name, data)
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
			d.Stop(download.StatusQueued)
		}
	}
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
}

func (e *Engine) Add(r AddRequest) (string, error) {
	r.URL = strings.TrimSpace(r.URL)
	if !strings.HasPrefix(r.URL, "http://") && !strings.HasPrefix(r.URL, "https://") {
		return "", errors.New("only http and https links are supported")
	}
	if r.UserAgent == "" {
		// Some servers drop clients with Go's default user agent.
		r.UserAgent = defaultUserAgent
	}
	e.mu.Lock()
	st := download.State{
		ID: newID(), URL: r.URL, PageURL: strings.TrimSpace(r.PageURL),
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

func (e *Engine) Remove(id string, deleteFile bool) error {
	d, err := e.get(id)
	if err != nil {
		return err
	}
	d.Stop(download.StatusPaused)
	if deleteFile && d.Status() != download.StatusCompleted {
		os.Remove(d.Path())
	}
	e.mu.Lock()
	delete(e.downloads, id)
	delete(e.dirty, id)
	for k := range e.resolves {
		if strings.HasPrefix(k, id+"|") {
			delete(e.resolves, k)
		}
	}
	for i, x := range e.order {
		if x == id {
			e.order = append(e.order[:i], e.order[i+1:]...)
			break
		}
	}
	e.mu.Unlock()
	os.Remove(e.statePath(id))
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
}

type StateEvent struct {
	Downloads  []download.Summary `json:"downloads"`
	Interfaces []IfaceView        `json:"interfaces"`
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
		e.eta = -1
	} else if now.Sub(e.etaAt) >= 5*time.Second {
		e.etaAt = now
		e.eta = -1
		if r := e.meter.Rate(); r > 0 && remaining > 0 {
			e.eta = float64(remaining) / r
		}
	}

	mainID := e.mainIDLocked()
	views := []IfaceView{}
	for id, ent := range e.ifaces {
		views = append(views, IfaceView{
			ID: id, Name: ent.info.Name, Description: ent.info.Description, IP: ent.info.IPv4.String(),
			Present: ent.present, Enabled: e.enabledLocked(ent), Metered: ent.info.Metered, VPN: ent.info.VPN,
			IsDefault: ent.info.IsDefault, IsMain: id == mainID, Connections: e.connsLocked(id),
			SocksPort: ent.socks.Port(), Speed: speeds[id],
		})
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Present != views[j].Present {
			return views[i].Present
		}
		return views[i].Name < views[j].Name
	})
	e.snapshot = StateEvent{Downloads: sums, Interfaces: views, Speed: total, ETA: e.eta, Active: active}
}

func (e *Engine) Snapshot() StateEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snapshot
}
