package torrent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	atorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"

	"downloadmanagersquared/engine/internal/archive"
	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/rate"
)

// Statuses a torrent has beyond an HTTP download's, defined with the others.
const (
	StatusMetadata = download.StatusMetadata
	StatusChoosing = download.StatusChoosing
	StatusChecking = download.StatusChecking
	StatusSeeding  = download.StatusSeeding
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
	VPN      paths.Mode      `json:"vpn,omitempty"`      // which session runs it; a new mode means a new Torrent there
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
	upSaved time.Time // when a changed upload count last marked the entry dirty

	verified atomic.Bool // its data was hashed since the engine started

	// What the swarm looks like, as of the last poll.
	paths               map[string]*pathRate
	peers, seeds, swarm int

	maxConns           int   // the peer limit at gave last: only run's goroutine reads or writes it
	wantDone, wantSize int64 // of the files to download, as of the last poll
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
		Kind: download.KindTorrent, ID: t.st.ID, URL: t.st.Magnet, Filename: t.st.Name, Dir: t.st.Dir,
		Size: t.st.Size, Status: t.st.Status, Error: t.st.Error, Speed: t.speed.Value(), ETA: -1,
		Links: map[string]download.LinkSummary{}, Added: t.st.Added, Finished: t.st.Finished,
		Uploaded: t.st.Uploaded, VPN: t.st.VPN, AskVPN: t.st.AskVPN, InfoHash: t.hash, WaitVPN: t.st.WaitVPN,
	}
	if t.st.Status == StatusChoosing {
		s.Files = t.filesLocked()
		s.FileCount = len(s.Files)
		for _, f := range s.Files {
			s.FilesSize += f.Size
		}
	}
	if t.st.Size > 0 {
		s.Ratio = float64(t.st.Uploaded) / float64(t.st.Size)
	}
	switch {
	case !t.st.Finished.IsZero():
		s.Done = t.st.Size
		s.Speed = 0
	case t.t != nil && t.t.Info() != nil:
		s.Done = t.wantDone
		if s.Speed > 0 {
			s.ETA = float64(s.Size-s.Done) / s.Speed
		}
	}
	if t.active {
		s.InSwarm = true
		s.UpSpeed = t.upSpeed.Value()
		s.Peers, s.Seeds, s.Swarm = t.peers, t.seeds, t.swarm
		for id, p := range t.paths {
			if p.peers > 0 {
				s.Links[id] = download.LinkSummary{Status: download.LinkReady, Conns: p.peers,
					Speed: p.downRate.Value(), Up: p.upRate.Value()}
			}
		}
	}
	return download.Present(s)
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
	var missed []string
	spec.Trackers, missed = resolveUDPTrackers(ctx, spec.Trackers, t.s.lookupFirst)
	t.mu.Lock()
	defer t.mu.Unlock()
	if ctx.Err() != nil {
		t.s.release()
		return nil, ctx.Err()
	}
	t.held = true
	spec.InfoBytes = t.st.Info
	t.stor = storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir: t.st.Dir, PieceCompletion: t.s.completion(),
		FilePathMaker: func(o storage.FilePathMakerOpts) string { return filepath.Join(diskParts(o.Info, o.File)...) },
		Logger:        slog.New(slog.DiscardHandler),
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
	t.maxConns = 0
	t.followMaxPeers(at)
	if len(missed) > 0 {
		go resolveLater(ctx, missed, t.s.lookupFirst, at.AddTrackers)
	}
	return at, nil
}

func (t *Torrent) run(ctx context.Context) {
	at, err := t.join(ctx)
	if err != nil {
		t.mu.Lock()
		if ctx.Err() == nil {
			t.dropLocked()
			t.st.Status, t.st.Error = download.StatusFailed, download.UserText(err)
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
		t.st.Name, t.st.Size = diskName(at.Info()), at.Length()
		t.st.Info = at.Metainfo().InfoBytes
		t.st.Status = StatusChecking
		if p := samePath(diskPaths(t.st.Dir, at.Info())); p != "" {
			t.dropLocked()
			t.st.Status = download.StatusFailed
			t.st.Error = "Two of its files would have the same name on Windows (" + filepath.Base(p) + ")"
		} else if t.st.Choose {
			t.dropLocked()
			t.st.Status = StatusChoosing
		}
	}) {
		return
	}
	if s := t.State().Status; s == StatusChoosing || s == download.StatusFailed {
		return
	}
	// The file storage trusts a full-size file and distrusts a .part one without
	// reading either, so hash whatever is on disk unless the pieces kept as
	// complete can be trusted.
	if t.hasData(at) && t.mustVerify(at) {
		t.verified.Store(true)
		go at.VerifyDataContext(ctx)
		select { // let it queue the first pieces
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
		}
	}
	if !t.poll(ctx, at, pollEvery, func() bool { return !checking(at) }) {
		return
	}
	complete := func() bool { // reads the choice afresh: SetFiles may change it while this runs
		t.mu.Lock()
		defer t.mu.Unlock()
		return t.wantDone == t.wantSize
	}
	if !complete() {
		if !t.update(at, func() { t.st.Status = download.StatusRunning }) {
			return
		}
		t.mu.Lock()
		applySkip(at, t.st.Skip)
		t.mu.Unlock()
		if !t.poll(ctx, at, pollEvery, complete) {
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
	if !t.poll(ctx, at, seedEvery, func() bool {
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
	dhtSoon    = 30 * time.Second // when it announced nowhere: no interface yet, or every DHT refused
	dhtTimeout = 2 * time.Minute
)

// announceDHT looks the torrent up on every interface's DHT, and announces it
// there, until ctx ends. Private torrents stay off the DHT (a magnet link's
// torrent counts as public until its info says otherwise).
func (t *Torrent) announceDHT(ctx context.Context, at *atorrent.Torrent) {
	for {
		next := dhtSoon
		for _, ds := range t.s.dhtServers() {
			if private(at) || ctx.Err() != nil {
				break
			}
			done, stop, err := at.AnnounceToDht(ds)
			if err != nil {
				continue
			}
			next = dhtEvery
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
		case <-time.After(next):
		}
	}
}

// upSaveEvery is how seldom a changed upload count alone is worth a state write.
const upSaveEvery = 30 * time.Second

// now is the clock update throttles by; tests replace it.
var now = time.Now

// update changes the state under the lock, unless the torrent left the swarm
// (at is no longer its torrent), and reports whether it did. It marks the
// entry dirty only if a saved field changed; the upload count alone counts at
// most every upSaveEvery, so an idle seeder does not rewrite its state file.
func (t *Torrent) update(at *atorrent.Torrent, f func()) bool {
	t.mu.Lock()
	if t.t != at {
		t.mu.Unlock()
		return false
	}
	old := t.st
	f()
	save := old.Status != t.st.Status || old.Error != t.st.Error || old.Size != t.st.Size ||
		len(old.Info) != len(t.st.Info) || !slices.Equal(old.Skip, t.st.Skip)
	if !save && old.Uploaded != t.st.Uploaded && now().Sub(t.upSaved) >= upSaveEvery {
		save = true
	}
	if save {
		t.upSaved = now()
	}
	t.mu.Unlock()
	if save {
		t.changed()
	}
	return true
}

// A seeding torrent only has the ratio and the upload speed to keep, so it polls slower.
const (
	pollEvery = 500 * time.Millisecond
	seedEvery = 2 * time.Second
)

// followMaxPeers sets at's peer limit to the session's, when it changed: the
// setting, or the interfaces in use.
func (t *Torrent) followMaxPeers(at *atorrent.Torrent) {
	if m := t.s.maxPeers(); m != t.maxConns {
		at.SetMaxEstablishedConns(m)
		t.maxConns = m
	}
}

// poll keeps the speeds and upload count current until done reports true, and
// reports false if the torrent left the swarm first.
func (t *Torrent) poll(ctx context.Context, at *atorrent.Torrent, every time.Duration, done func() bool) bool {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		t.followMaxPeers(at)
		stats := at.Stats()
		completed, uploaded := at.BytesCompleted(), stats.BytesWrittenData.Int64()
		byPath := t.s.peersByIface(at)
		if !t.update(at, func() {
			if at.Info() != nil { // the one walk of the files: Summary and the finish check read it
				t.wantDone, t.wantSize = wanted(at, t.st.Skip)
			}
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
	if len(skip) == 0 { // no per-file walk: it takes the client lock per file
		return at.BytesCompleted(), at.Length()
	}
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
	skip, size, err := choice(all, files)
	if err != nil {
		t.mu.Unlock()
		return err
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

// choice turns the ticked files into the indexes to skip and the size wanted.
func choice(all []metainfo.FileInfo, files []bool) (skip []int, size int64, err error) {
	if len(files) != len(all) {
		return nil, 0, fmt.Errorf("got a choice for %d files, the torrent has %d", len(files), len(all))
	}
	for i, on := range files {
		if on {
			size += all[i].Length
		} else {
			skip = append(skip, i)
		}
	}
	if len(skip) == len(files) {
		return nil, 0, errors.New("choose at least one file")
	}
	return skip, size, nil
}

// applySkip tells the swarm which files to fetch.
func applySkip(at *atorrent.Torrent, skip []int) {
	for i, f := range at.Files() {
		if slices.Contains(skip, i) {
			f.SetPriority(atorrent.PiecePriorityNone)
		} else {
			f.Download()
		}
	}
}

// SetFiles changes which files to download (one flag per file) after the
// choice was made, also while the torrent runs. Once the whole download is
// done it is refused: a finished torrent seeds what it has.
func (t *Torrent) SetFiles(files []bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.st.Status == StatusChoosing || !t.st.Finished.IsZero() {
		return errors.New("the files of this torrent can't be changed now")
	}
	info, err := t.infoLocked()
	if err != nil {
		return err
	}
	skip, size, err := choice(info.UpvertedFiles(), files)
	if err != nil {
		return err
	}
	t.st.Skip, t.st.Size = skip, size
	// Only while downloading: before that, run applies the choice itself.
	if t.t != nil && t.t.Info() != nil && t.st.Status == download.StatusRunning {
		applySkip(t.t, skip)
	}
	go t.changed()
	return nil
}

// Files lists the files with how much of each is done.
func (t *Torrent) Files() []download.FileSummary {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.filesLocked()
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
		fs := download.FileSummary{Path: p, Size: f.Length, Selected: !slices.Contains(t.st.Skip, i)}
		if t.t != nil && t.t.Info() != nil && i < len(t.t.Files()) {
			fs.Done = t.t.Files()[i].BytesCompleted()
		}
		out = append(out, fs)
	}
	return out
}

// hasData is whether any of the torrent's files, finished or partial, is on disk.
// mustVerify: the pieces' completion as kept can't be trusted for what is on
// disk. It isn't kept between runs (no Completion), the engine last stopped
// uncleanly and this torrent wasn't hashed since (a piece is marked complete
// before its bytes are flushed), or nothing is kept of this torrent.
func (t *Torrent) mustVerify(at *atorrent.Torrent) bool {
	pc := t.s.cfg.Completion
	if pc == nil || t.s.cfg.Unclean && !t.verified.Load() {
		return true
	}
	c, err := pc.Get(metainfo.PieceKey{InfoHash: at.InfoHash(), Index: 0})
	return err != nil || !c.Ok
}

func (t *Torrent) hasData(at *atorrent.Torrent) bool {
	t.mu.Lock()
	dir := t.st.Dir
	t.mu.Unlock()
	for _, p := range diskPaths(dir, at.Info()) {
		if p == "" {
			continue
		}
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

// maxPeers is how many peers a torrent may have connected: the setting for
// each interface, as peers spread over them and using several lines at once is
// the point. Torrents follow it as it changes (Torrent.followMaxPeers).
func (s *Session) maxPeers() int {
	n := 50 // the client's own default
	if s.cfg.PeersPerLine != nil {
		if v := s.cfg.PeersPerLine(); v > 0 {
			n = v
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return n * max(1, len(s.ifs))
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

// Resume lets a stopped torrent run again: queued, or once its download
// completed, seeding, which takes no queue slot.
func (t *Torrent) Resume() {
	if !t.State().Finished.IsZero() {
		t.Stop(StatusSeeding)
		return
	}
	t.Stop(download.StatusQueued)
}

// Discard stops the torrent for good. With deleteFiles, unfinished files go;
// once its download completed (seeding, or checking to seed again) they are the user's.
// Only the torrent's own files go, never a folder by name: its name may be
// that of a folder the user already had.
func (t *Torrent) Discard(deleteFiles bool) {
	finished := t.Status() == download.StatusCompleted || !t.State().Finished.IsZero()
	t.Stop(download.StatusPaused)
	if !deleteFiles || finished {
		return
	}
	p := t.Path()
	t.mu.Lock()
	info, err := t.infoLocked()
	dir, skip := filepath.Clean(t.st.Dir), t.st.Skip
	t.mu.Unlock()
	if err != nil {
		if p != "" {
			os.Remove(p + ".part") // files unknown: at most a single file's .part, never a folder
		}
		return
	}
	for i, p := range diskPaths(dir, info) {
		if p == "" {
			continue
		}
		os.Remove(p + ".part") // a file waits under .part until complete
		if !slices.Contains(skip, i) {
			os.Remove(p) // a skipped file of that name was never the torrent's
		}
		// Folders the torrent's files leave empty go too, up to the save folder.
		for d := filepath.Dir(p); len(d) > len(dir) && os.Remove(d) == nil; d = filepath.Dir(d) {
		}
	}
}

// diskPaths are where the file storage keeps the torrent's files under dir,
// in the torrent's order: dir/name for one file, dir/name/path for several.
// A path that would leave dir (peers choose them) is "": the storage refuses
// to open it, and Discard must not delete it.
func diskPaths(dir string, info *metainfo.Info) []string {
	var out []string
	for _, f := range info.UpvertedFiles() {
		p := filepath.Join(append([]string{dir}, diskParts(info, &f)...)...)
		if rel, err := filepath.Rel(dir, p); err != nil || !filepath.IsLocal(rel) {
			p = ""
		}
		out = append(out, p)
	}
	return out
}

// diskParts is file f's path under the save folder, the storage's
// FilePathMaker: the torrent's name, then f's path, each part cleaned for
// Windows. Peers choose them, and a ':' left in would write the data to an
// alternate data stream of another file. Nothing is cut short (as
// SanitizeFilename cuts at 200 bytes): two long names that differ only at
// their end would become one file. So a name that worked before is unchanged,
// and a torrent fetched under it keeps its data.
func diskParts(info *metainfo.Info, f *metainfo.FileInfo) []string {
	var parts []string
	if n := diskName(info); n != "" {
		parts = append(parts, n)
	}
	for _, p := range f.BestPath() {
		parts = append(parts, diskPart(p))
	}
	return parts
}

// diskName is the torrent's name on disk (st.Name): "" when it has none.
func diskName(info *metainfo.Info) string {
	if n := info.BestName(); n != metainfo.NoName {
		return diskPart(n)
	}
	return ""
}

func diskPart(p string) string {
	return cmp.Or(archive.CleanPart(strings.NewReplacer(`/`, "_", `\`, "_").Replace(p)), "_")
}

// samePath is a path two of the torrent's files would share once cleaned
// ("a?b" and "a*b" are both "a_b"; Windows ignores case), "" when none do.
func samePath(paths []string) string {
	seen := map[string]bool{}
	for _, p := range paths {
		k := strings.ToLower(p)
		if p != "" && seen[k] {
			return p
		}
		seen[k] = true
	}
	return ""
}

// Yields is true: torrents queue behind HTTP downloads and give their slot to one.
func (t *Torrent) Yields() bool { return true }

// Retrying is false: a torrent with no peers yet still holds its slot, as it
// gives it to any queued HTTP download anyway.
func (t *Torrent) Retrying() bool { return false }

// Rejoins is whether the torrent should rejoin the swarm without a slot:
// seeding, or fetching its metadata.
func (t *Torrent) Rejoins() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.active && (t.st.Status == StatusSeeding || t.st.Status == StatusMetadata)
}

// Added is when the torrent was added.
// SourceURL is the magnet link the torrent was added from.
func (t *Torrent) SourceURL() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.st.Magnet
}

func (t *Torrent) Added() time.Time { return t.st.Added } // fixed when added: no lock needed

// Saved is the torrent's kind and the state that resumes it after a restart.
func (t *Torrent) Saved() (string, any) { return "torrents", t.State() }

// VPN is the torrent's VPN mode.
func (t *Torrent) VPN() paths.Mode { return t.st.VPN } // fixed for this Torrent: no lock needed

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
	t.st.Error = "Waiting for the VPN: nothing is sent outside it. It resumes by itself once the VPN is connected."
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
