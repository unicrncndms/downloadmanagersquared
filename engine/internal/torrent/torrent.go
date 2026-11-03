package torrent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	atorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/rate"
)

// Statuses a torrent has beyond an HTTP download's. A finished torrent (done
// seeding) is download.StatusCompleted.
const (
	StatusMetadata download.Status = "metadata" // a magnet link learning the torrent's files from peers
	StatusChoosing download.Status = "choosing" // waiting for the user to pick files and a folder
	StatusChecking download.Status = "checking" // hashing data already on disk
	StatusSeeding  download.Status = "seeding"  // complete, uploading until the ratio target
)

// State is everything needed to resume a torrent after a restart.
type State struct {
	ID       string          `json:"id"`
	Magnet   string          `json:"magnet"`
	Info     []byte          `json:"info,omitempty"` // bencoded info dict, once known
	Dir      string          `json:"dir"`
	Choose   bool            `json:"choose,omitempty"` // the user picks files and folder before it downloads
	AskVPN   bool            `json:"askVpn,omitempty"` // the user picks VPN or not before it joins the swarm
	Skip     []int           `json:"skip,omitempty"`   // indexes of the files not to download
	Name     string          `json:"name,omitempty"`
	Size     int64           `json:"size"`               // of the files to download
	Uploaded int64           `json:"uploaded,omitempty"` // across every run
	VPN      paths.Mode      `json:"vpn,omitempty"`      // fixed when added: which session runs it
	WaitVPN  bool            `json:"waitVpn,omitempty"`  // paused because no tunnel is up, under VPN only
	Status   download.Status `json:"status"`
	Error    string          `json:"error,omitempty"`
	Added    time.Time       `json:"added"`
	Finished time.Time       `json:"finished,omitzero"` // when the download completed
}

type Torrent struct {
	s *Session

	mu      sync.Mutex
	st      State
	t       *atorrent.Torrent // nil unless in the swarm
	stor    storage.ClientImplCloser
	cancel  context.CancelFunc
	active  bool  // in the swarm, in any phase
	held    bool  // holds the session's client (acquire, matched by release)
	slot    bool  // checking or downloading after the queue started it: holds a queue slot
	upBase  int64 // st.Uploaded when this run joined the swarm
	speed   rate.EWMA
	upSpeed rate.EWMA
	info    *metainfo.Info // st.Info parsed, once needed
	hash    string

	// What the swarm looks like, as of the last poll.
	paths               map[string]*pathRate
	peers, seeds, swarm int
}

// pathRate is a torrent's traffic on one interface.
type pathRate struct {
	peers            int
	read, written    int64 // as the connected peers last counted them
	down, up         int64 // only growing: a peer leaving takes its count with it
	downRate, upRate rate.EWMA
}

// Add registers a torrent with the session without starting it.
func (s *Session) Add(st State) (*Torrent, error) {
	spec, err := atorrent.TorrentSpecFromMagnetUri(st.Magnet)
	if err != nil {
		return nil, err
	}
	if st.Status == "" {
		st.Status = download.StatusQueued
	}
	if st.Name == "" {
		st.Name = download.SanitizeFilename(spec.DisplayName)
	}
	if st.Size == 0 {
		st.Size = -1 // unknown until the metadata arrives
	}
	return &Torrent{s: s, st: st, hash: spec.InfoHash.HexString()}, nil
}

// Path is the torrent's file, or its folder when it has several; "" while its
// name is unknown, so nothing ever points at the download folder itself.
func (t *Torrent) Path() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.st.Name == "" {
		return ""
	}
	return filepath.Join(t.st.Dir, t.st.Name)
}

// Summary is what the UI shows: one row like an HTTP download, with each
// interface's peers where an HTTP download has its connections.
func (t *Torrent) Summary() download.Summary {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := download.Summary{
		Kind: "torrent", ID: t.st.ID, URL: t.st.Magnet, Filename: t.st.Name, Dir: t.st.Dir,
		Size: t.st.Size, Status: t.st.Status, Error: t.st.Error, Speed: t.speed.Value(), ETA: -1,
		Links: map[string]download.LinkSummary{}, Added: t.st.Added, Finished: t.st.Finished,
		Uploaded: t.st.Uploaded, VPN: t.st.VPN, AskVPN: t.st.AskVPN, InfoHash: t.hash,
	}
	if t.st.Status == StatusChoosing {
		s.Files = t.filesLocked()
	}
	if t.st.Size > 0 {
		s.Ratio = float64(t.st.Uploaded) / float64(t.st.Size)
	}
	switch {
	case !t.st.Finished.IsZero():
		s.Done = t.st.Size
		s.Speed = 0
	case t.t != nil && t.t.Info() != nil:
		s.Done, _ = wanted(t.t, t.st.Skip)
		if s.Speed > 0 {
			s.ETA = float64(s.Size-s.Done) / s.Speed
		}
	}
	if t.active {
		s.UpSpeed = t.upSpeed.Value()
		s.Peers, s.Seeds, s.Swarm = t.peers, t.seeds, t.swarm
		for id, p := range t.paths {
			if p.peers > 0 {
				s.Links[id] = download.LinkSummary{Status: download.LinkReady, Conns: p.peers,
					Speed: p.downRate.Value(), Up: p.upRate.Value()}
			}
		}
	}
	return s
}

func (t *Torrent) ID() string { return t.st.ID }

func (t *Torrent) Status() download.Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.st.Status
}

// Running is whether the torrent holds a queue slot: checking or downloading.
// Seeding holds none.
func (t *Torrent) Running() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.slot
}

// Active is whether the torrent is in the swarm, in any phase.
func (t *Torrent) Active() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active
}

func (t *Torrent) State() State {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.st
}

// Start joins the swarm. Queued, it downloads the chosen files, then seeds.
// Seeding (after a restart), it checks its data and seeds again. Still to be
// chosen, it only learns the files (if it doesn't know them) and waits in
// StatusChoosing. Only checking and downloading hold a queue slot.
func (t *Torrent) Start() {
	t.mu.Lock()
	if t.active || t.st.Status == download.StatusCompleted {
		t.mu.Unlock()
		return
	}
	// Waiting for the user: the files are known, or the VPN question comes first
	// (learning the files from peers already joins the swarm).
	if t.st.AskVPN || t.st.Choose && t.st.Info != nil {
		t.st.Status = StatusChoosing
		t.mu.Unlock()
		t.changed()
		return
	}
	defer t.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel, t.active = cancel, true
	t.slot = !t.st.Choose && t.st.Status != StatusSeeding
	t.st.Error, t.st.WaitVPN = "", false
	if t.st.Info == nil {
		t.st.Status = StatusMetadata
	} else {
		t.st.Status = StatusChecking
	}
	go t.run(ctx)
}

// join adds the torrent to the client, once its UDP trackers are resolved.
func (t *Torrent) join(ctx context.Context) (*atorrent.Torrent, error) {
	spec, err := atorrent.TorrentSpecFromMagnetUri(t.st.Magnet)
	if err != nil {
		return nil, err
	}
	cl, err := t.s.acquire() // the session's sockets open now, if no other torrent opened them
	if err != nil {
		return nil, err
	}
	spec.Trackers = resolveUDPTrackers(ctx, spec.Trackers, t.s.lookupFirst)
	t.mu.Lock()
	defer t.mu.Unlock()
	if ctx.Err() != nil {
		t.s.release()
		return nil, ctx.Err()
	}
	t.held = true
	spec.InfoBytes = t.st.Info
	t.stor = storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir: t.st.Dir, PieceCompletion: storage.NewMapPieceCompletion(),
		Logger: slog.New(slog.DiscardHandler),
	})
	spec.Storage = t.stor
	at, _, err := cl.AddTorrentSpec(spec)
	if err != nil {
		t.stor.Close()
		t.stor = nil
		t.held = false
		t.s.release()
		return nil, err
	}
	t.t, t.upBase = at, t.st.Uploaded
	return at, nil
}

func (t *Torrent) run(ctx context.Context) {
	at, err := t.join(ctx)
	if err != nil {
		t.mu.Lock()
		if ctx.Err() == nil {
			t.dropLocked()
			t.st.Status, t.st.Error = download.StatusFailed, err.Error()
		}
		t.mu.Unlock()
		t.changed()
		return
	}
	t.changed()
	go t.announceDHT(ctx, at)
	select {
	case <-at.GotInfo():
	case <-ctx.Done():
		return
	}
	if !t.update(at, func() {
		// Peers choose the name; never let it be a path.
		t.st.Name, t.st.Size = download.SanitizeFilename(at.Name()), at.Length()
		t.st.Info = at.Metainfo().InfoBytes
		t.st.Status = StatusChecking
		if t.st.Choose {
			t.dropLocked()
			t.st.Status = StatusChoosing
		}
	}) {
		return
	}
	if t.State().Status == StatusChoosing {
		return
	}
	// The file storage trusts a full-size file and distrusts a .part one without
	// reading either, so hash whatever is on disk.
	if t.hasData(at) {
		go at.VerifyDataContext(ctx)
		time.Sleep(50 * time.Millisecond) // let it queue the first pieces
	}
	if !t.poll(ctx, at, func() bool { return !checking(at) }) {
		return
	}
	t.mu.Lock()
	skip := t.st.Skip
	t.mu.Unlock()
	complete := func() bool {
		done, size := wanted(at, skip)
		return done == size
	}
	if !complete() {
		if !t.update(at, func() { t.st.Status = download.StatusRunning }) {
			return
		}
		for i, f := range at.Files() {
			if slices.Contains(skip, i) {
				f.SetPriority(atorrent.PiecePriorityNone)
			} else {
				f.Download()
			}
		}
		if !t.poll(ctx, at, complete) {
			return
		}
	}
	notify := false
	if !t.update(at, func() {
		notify = t.st.Finished.IsZero()
		if notify {
			t.st.Finished = time.Now()
		}
		t.st.Status, t.slot = StatusSeeding, false
	}) {
		return
	}
	if f := t.s.cfg.Finished; notify && f != nil {
		f(t)
	}
	if !t.poll(ctx, at, func() bool {
		t.mu.Lock()
		defer t.mu.Unlock()
		return t.st.Size > 0 && float64(t.st.Uploaded) >= t.s.ratio()*float64(t.st.Size)
	}) {
		return
	}
	t.update(at, func() {
		t.dropLocked()
		t.st.Status = download.StatusCompleted
	})
}

const (
	dhtEvery   = 10 * time.Minute
	dhtTimeout = 2 * time.Minute
)

// announceDHT looks the torrent up on every interface's DHT, and announces it
// there, until ctx ends. Private torrents stay off the DHT (a magnet link's
// torrent counts as public until its info says otherwise).
func (t *Torrent) announceDHT(ctx context.Context, at *atorrent.Torrent) {
	for {
		for _, ds := range t.s.dhtServers() {
			if private(at) {
				break
			}
			done, stop, err := at.AnnounceToDht(ds)
			if err != nil {
				continue
			}
			select {
			case <-done:
			case <-time.After(dhtTimeout):
			case <-ctx.Done():
			}
			stop()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(dhtEvery):
		}
	}
}

// update changes the state under the lock, unless the torrent left the swarm
// (at is no longer its torrent), and reports whether it did.
func (t *Torrent) update(at *atorrent.Torrent, f func()) bool {
	t.mu.Lock()
	if t.t != at {
		t.mu.Unlock()
		return false
	}
	f()
	t.mu.Unlock()
	t.changed()
	return true
}

// poll keeps the speeds and upload count current until done reports true, and
// reports false if the torrent left the swarm first.
func (t *Torrent) poll(ctx context.Context, at *atorrent.Torrent, done func() bool) bool {
	const every = 500 * time.Millisecond
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		stats := at.Stats()
		completed, uploaded := at.BytesCompleted(), stats.BytesWrittenData.Int64()
		byPath := t.s.peersByIface(at)
		if !t.update(at, func() {
			t.speed.Update(completed, every)
			t.upSpeed.Update(uploaded, every)
			t.st.Uploaded = t.upBase + uploaded
			t.peers, t.seeds, t.swarm = stats.ActivePeers, stats.ConnectedSeeders, stats.TotalPeers
			t.ratesLocked(byPath, every)
		}) {
			return false
		}
		if done() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
}

// ratesLocked turns each interface's byte counts into speeds.
func (t *Torrent) ratesLocked(byPath map[string]ifacePeers, every time.Duration) {
	if t.paths == nil {
		t.paths = map[string]*pathRate{}
	}
	for id := range t.paths {
		if _, ok := byPath[id]; !ok {
			byPath[id] = ifacePeers{} // no peers left: speeds wind down
		}
	}
	for id, now := range byPath {
		p := t.paths[id]
		if p == nil {
			p = &pathRate{}
			t.paths[id] = p
		}
		p.down += max(now.read-p.read, 0)
		p.up += max(now.written-p.written, 0)
		p.peers, p.read, p.written = now.n, now.read, now.written
		p.downRate.Update(p.down, every)
		p.upRate.Update(p.up, every)
	}
}

// wanted is how much of the files to download is done, and their size.
func wanted(at *atorrent.Torrent, skip []int) (done, size int64) {
	for i, f := range at.Files() {
		if !slices.Contains(skip, i) {
			done += f.BytesCompleted()
			size += f.Length()
		}
	}
	return done, size
}

// Choose sets which files to download (one flag per file, in the torrent's
// order) and the folder to save into ("" keeps the current one), and queues the
// torrent. Only a torrent waiting in StatusChoosing takes a choice.
func (t *Torrent) Choose(files []bool, dir string) error {
	t.mu.Lock()
	if t.st.Status != StatusChoosing {
		t.mu.Unlock()
		return errors.New("the files of this torrent can't be chosen now")
	}
	info, err := t.infoLocked()
	if err != nil {
		t.mu.Unlock()
		return err
	}
	all := info.UpvertedFiles()
	if len(files) != len(all) {
		t.mu.Unlock()
		return fmt.Errorf("got a choice for %d files, the torrent has %d", len(files), len(all))
	}
	var skip []int
	var size int64
	for i, on := range files {
		if on {
			size += all[i].Length
		} else {
			skip = append(skip, i)
		}
	}
	if len(skip) == len(files) {
		t.mu.Unlock()
		return errors.New("choose at least one file")
	}
	if dir != "" {
		t.st.Dir = dir
	}
	t.st.Skip, t.st.Size, t.st.Choose = skip, size, false
	t.st.Status = download.StatusQueued
	t.mu.Unlock()
	t.changed()
	return nil
}

// infoLocked parses the kept info dict.
func (t *Torrent) infoLocked() (*metainfo.Info, error) {
	if t.info == nil {
		if t.st.Info == nil {
			return nil, errors.New("the torrent's files aren't known yet")
		}
		var info metainfo.Info
		if err := bencode.Unmarshal(t.st.Info, &info); err != nil {
			return nil, err
		}
		t.info = &info
	}
	return t.info, nil
}

// filesLocked lists the torrent's files for the add step, nil while unknown.
func (t *Torrent) filesLocked() []download.FileSummary {
	info, err := t.infoLocked()
	if err != nil {
		return nil
	}
	var out []download.FileSummary
	for i, f := range info.UpvertedFiles() {
		p := info.BestName()
		if info.IsDir() {
			p = strings.Join(f.BestPath(), "/")
		}
		out = append(out, download.FileSummary{Path: p, Size: f.Length, Selected: !slices.Contains(t.st.Skip, i)})
	}
	return out
}

// hasData is whether any of the torrent's files, finished or partial, is on disk.
func (t *Torrent) hasData(at *atorrent.Torrent) bool {
	t.mu.Lock()
	dir := t.st.Dir
	t.mu.Unlock()
	for _, f := range at.Files() {
		p := filepath.Join(dir, filepath.FromSlash(f.Path()))
		for _, q := range []string{p, p + ".part"} {
			if _, err := os.Stat(q); err == nil {
				return true
			}
		}
	}
	return false
}

// checking is whether any piece is being hashed or waits to be.
func checking(at *atorrent.Torrent) bool {
	for _, r := range at.PieceStateRuns() {
		if r.Checking {
			return true
		}
	}
	return false
}

func (s *Session) ratio() float64 {
	if s.cfg.Ratio == nil {
		return 0
	}
	return s.cfg.Ratio()
}

// Stop leaves the swarm; as is the status to show afterwards (paused or queued).
func (t *Torrent) Stop(as download.Status) {
	t.mu.Lock()
	if t.active {
		t.dropLocked()
	}
	// Already stopped, only the status changes, which is how resume re-queues.
	if t.st.Status != download.StatusCompleted {
		t.st.Status, t.st.Error, t.st.WaitVPN = as, "", false
	}
	t.mu.Unlock()
	t.changed()
}

// VPN is the torrent's VPN mode.
func (t *Torrent) VPN() paths.Mode { return t.st.VPN } // fixed when added: no lock needed

// PauseForVPN leaves the swarm because no tunnel is up, under VPN only; the
// engine queues it again once one is. Only a torrent in the swarm or waiting
// to join it pauses: one the user paused, or that is finished, stays as it is.
func (t *Torrent) PauseForVPN() {
	t.mu.Lock()
	if s := t.st.Status; !t.active && s != download.StatusQueued && s != StatusSeeding && s != StatusMetadata {
		t.mu.Unlock()
		return
	}
	if t.active {
		t.dropLocked()
	}
	t.st.Status, t.st.WaitVPN = download.StatusPaused, true
	t.st.Error = "Paused: no VPN tunnel is up. It resumes by itself once one is."
	t.mu.Unlock()
	t.changed()
}

// WaitingForVPN is whether the torrent paused only because no tunnel was up.
func (t *Torrent) WaitingForVPN() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.st.Status == download.StatusPaused && t.st.WaitVPN
}

func (t *Torrent) dropLocked() {
	t.cancel()
	if t.t != nil {
		t.t.Drop()
		t.stor.Close()
	}
	t.t, t.stor, t.active, t.slot = nil, nil, false, false
	t.speed, t.upSpeed = rate.EWMA{}, rate.EWMA{}
	t.paths, t.peers, t.seeds, t.swarm = nil, 0, 0, 0
	if t.held {
		t.held = false
		t.s.release() // the last torrent out closes the session's sockets
	}
}

func (t *Torrent) changed() {
	if f := t.s.cfg.Changed; f != nil {
		f(t)
	}
}
