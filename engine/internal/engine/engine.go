// Package engine ties everything together: interfaces, downloads, the queue,
// persistence and the API that the extension (and later a GUI) talk to.
package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anacrolix/torrent/storage"
	"golang.org/x/sys/windows"

	"downloadmanagersquared/engine/internal/atomicfile"
	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/ipc"
	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/portmap"
	"downloadmanagersquared/engine/internal/queue"
	"downloadmanagersquared/engine/internal/rate"
	"downloadmanagersquared/engine/internal/torrent"
	"downloadmanagersquared/engine/internal/update"
	"downloadmanagersquared/engine/internal/vpn"
	"downloadmanagersquared/engine/internal/winutil"
)

var ErrAlreadyRunning = errors.New("engine already running")

// Version is the release, set at build time from the git tag (build.ps1 -Version).
var Version = "dev"

const defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:140.0) Gecko/20100101 Firefox/140.0"

type Engine struct {
	dir string
	ln  net.Listener
	// One torrent session per VPN mode: a session's sockets are only the paths
	// its mode allows, so a VPN-only torrent has no bare socket to leak through.
	sessions map[paths.Mode]*torrent.Session
	queue    *queue.Queue
	setsMu   sync.Mutex
	sets     map[string]*download.Set // split archives, by key
	tails    map[string]string        // the id of the part each set had start first last tick, for its end (syncSets)

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

	// unclean: the engine last stopped without Shutdown (a crash, a power
	// cut), so what it saved may be behind what was on disk.
	unclean bool
	pieces  storage.PieceCompletion // the torrents' pieces complete, kept between runs; nil if it can't be opened

	quit     chan struct{}
	quitOnce sync.Once
	running  atomic.Bool   // Run was called
	keeping  atomic.Value  // the archiveKeeping setting, a string, for ArchiveKeeping
	ran      chan struct{} // closed when Run returns
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
		ran:      make(chan struct{}),
		quitReq:  make(chan struct{}),
	}
	e.keepingNow()
	e.saveSettingsLocked() // keeps the torrent port picked on the first start
	e.unclean = markRunning(dir)
	if pc, err := storage.NewBoltPieceCompletion(filepath.Join(dir, "torrents")); err != nil {
		log.Printf("keeping the torrents' pieces: %v; their data is checked at each start", err)
	} else {
		e.pieces = pc
	}
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
			Port:       ports[m],
			Completion: e.pieces,
			Unclean:    e.unclean,
		})
		if err != nil {
			for _, s := range e.sessions {
				s.Close()
			}
			if e.pieces != nil {
				e.pieces.Close()
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

// Run serves the API and drives the queue until Shutdown.
func (e *Engine) Run() {
	e.running.Store(true)
	defer close(e.ran)
	if e.ln != nil {
		go ipc.Serve(e.ln, e)
	}
	go e.mapPorts()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	last := time.Now()
	n := 0
	var sent []byte // the state event last sent
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
			e.syncSets()
			e.schedule()
			e.queue.Save()
			e.updateSnapshot(now.Sub(last))
			last = now
			// Only a state that changed: idle, the clients redraw nothing. A new
			// client asks for the state itself (hello, getState).
			if b, err := ipc.EncodeEvent("state", e.Snapshot()); err == nil && !bytes.Equal(b, sent) {
				sent = b
				e.sendAll(b)
			}
		}
	}
}

// QuitRequested is closed when a client (an upgrade or the uninstaller) asks
// the engine to quit; the tray then quits as if from its menu.
func (e *Engine) QuitRequested() <-chan struct{} { return e.quitReq }

// QuitByUser tells the browsers the user quit the engine, so they don't start
// it again by reconnecting. An upgrade's quit doesn't: its new engine is wanted.
func (e *Engine) QuitByUser() { e.broadcast("quit", nil) }

// Shutdown saves everything, then lets go of the pipe: whoever waits for the
// pipe to be free finds the state on disk.
func (e *Engine) Shutdown() {
	e.quitOnce.Do(func() {
		close(e.quit)
		if e.running.Load() {
			// A tick under way would start downloads again after they are
			// stopped, and use the sessions after they are closed.
			<-e.ran
		}
		e.setsMu.Lock()
		for _, s := range e.sets {
			s.Stop() // first: what they freed is saved with the parts
		}
		e.setsMu.Unlock()
		e.queue.Close()
		if e.ln != nil {
			e.ln.Close()
		}
		for _, s := range e.sessions {
			s.Close()
		}
		if e.pieces != nil {
			e.pieces.Close()
		}
		e.vpn.Close()
		e.mu.Lock()
		for _, ent := range e.ifaces {
			ent.socks.Close()
		}
		e.mu.Unlock()
		markStopped(e.dir) // last: everything is saved
	})
}

// runningMarker is a file in the data folder that is there while an engine
// runs: found at the start, the last engine didn't stop cleanly.
const runningMarker = "running"

// markRunning marks the engine in dir as running, and says whether the last
// one stopped without Shutdown.
func markRunning(dir string) (unclean bool) {
	p := filepath.Join(dir, runningMarker)
	_, err := os.Stat(p)
	if err := atomicfile.Write(p, nil, 0o644); err != nil {
		log.Printf("marking the engine as running: %v", err)
	}
	return err == nil
}

// markStopped says the engine in dir stopped cleanly.
func markStopped(dir string) {
	if err := os.Remove(filepath.Join(dir, runningMarker)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("marking the engine as stopped: %v", err)
	}
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

func (e *Engine) ETAWindow() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return time.Duration(e.settings.ETAWindowSec) * time.Second
}

// ArchiveKeeping is download.Env's. Downloads ask for it under their own
// lock, so it reads a copy of the setting rather than take e.mu.
func (e *Engine) ArchiveKeeping() string {
	if k, ok := e.keeping.Load().(string); ok {
		return k
	}
	return download.KeepAuto
}

// keepingNow copies the archiveKeeping setting for ArchiveKeeping; with e.mu held.
func (e *Engine) keepingNow() { e.keeping.Store(e.settings.ArchiveKeeping) }

func (e *Engine) Changed(d *download.Download) { e.queue.MarkDirty(d.ID()) }

func (e *Engine) Save(d *download.Download) error { return e.queue.SaveNow(d.ID()) }

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
		if err := e.SetFolder(id, nil, &dir, nil); err != nil {
			log.Printf("download %s: the folder chosen: %v", id, err)
		}
	}()
	return nil
}

// SetFolder sets a download's folder to extract into, which becomes the last
// one used, and switches Download as folder on or off; either may be nil.
// A part of a split archive sets them for its whole Set.
func (e *Engine) SetFolder(id string, on *bool, into, keep *string) error {
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
	if keep != nil {
		if err := d.SetArchiveKeeping(*keep); err != nil {
			return err
		}
	}
	dir := ""
	if into != nil {
		dir = strings.TrimSpace(*into)
		if err := d.SetFolderInto(dir); err != nil {
			return err
		}
		e.applySettings(func(s *Settings) { s.useFolder(filepath.Clean(dir)) })
	}
	// The other parts of a split archive take the same: with another folder
	// they would no longer be one Set. One that can't (it is complete) stays.
	for _, m := range d.Mates()[1:] {
		if on != nil {
			m.SetFolder(*on)
		}
		if into != nil {
			m.SetFolderInto(dir)
		}
		if keep != nil {
			m.SetArchiveKeeping(*keep)
		}
	}
	return nil
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
