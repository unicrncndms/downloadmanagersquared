package torrent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	atorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/storage"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/rate"
)

// Statuses a torrent has beyond an HTTP download's. A finished torrent (done
// seeding) is download.StatusCompleted.
const (
	StatusMetadata download.Status = "metadata" // a magnet link learning the torrent's files from peers
	StatusChecking download.Status = "checking" // hashing data already on disk
	StatusSeeding  download.Status = "seeding"  // complete, uploading until the ratio target
)

// State is everything needed to resume a torrent after a restart.
type State struct {
	ID       string          `json:"id"`
	Magnet   string          `json:"magnet"`
	Info     []byte          `json:"info,omitempty"` // bencoded info dict, once known
	Dir      string          `json:"dir"`
	Name     string          `json:"name,omitempty"`
	Size     int64           `json:"size"`
	Uploaded int64           `json:"uploaded,omitempty"` // across every run
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
	slot    bool  // checking or downloading after the queue started it: holds a queue slot
	upBase  int64 // st.Uploaded when this run joined the swarm
	speed   rate.EWMA
	upSpeed rate.EWMA
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
	return &Torrent{s: s, st: st}, nil
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
		Uploaded: t.st.Uploaded,
	}
	switch {
	case !t.st.Finished.IsZero():
		s.Done = t.st.Size
		s.Speed = 0
		s.UpSpeed = t.upSpeed.Value()
		if t.st.Size > 0 {
			s.Ratio = float64(t.st.Uploaded) / float64(t.st.Size)
		}
	case t.t != nil && t.t.Info() != nil:
		s.Done = t.t.BytesCompleted()
		if s.Speed > 0 {
			s.ETA = float64(s.Size-s.Done) / s.Speed
		}
	}
	if t.t != nil {
		for id, n := range t.s.peersByIface(t.t) {
			s.Links[id] = download.LinkSummary{Status: download.LinkReady, Conns: n}
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

// Start joins the swarm: queued, it downloads everything, then seeds; seeding
// (after a restart), it checks its data and seeds again without a queue slot.
func (t *Torrent) Start() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active || t.st.Status == download.StatusCompleted {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel, t.active = cancel, true
	t.slot = t.st.Status != StatusSeeding
	t.st.Error = ""
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
	spec.Trackers = resolveUDPTrackers(ctx, spec.Trackers, t.s.lookupFirst)
	t.mu.Lock()
	defer t.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	spec.InfoBytes = t.st.Info
	t.stor = storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir: t.st.Dir, PieceCompletion: storage.NewMapPieceCompletion(),
		Logger: slog.New(slog.DiscardHandler),
	})
	spec.Storage = t.stor
	at, _, err := t.s.cl.AddTorrentSpec(spec)
	if err != nil {
		t.stor.Close()
		t.stor = nil
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
	}) {
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
	if at.BytesCompleted() < at.Length() {
		if !t.update(at, func() { t.st.Status = download.StatusRunning }) {
			return
		}
		at.DownloadAll()
		if !t.poll(ctx, at, func() bool { return at.BytesCompleted() == at.Length() }) {
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
		if !t.update(at, func() {
			t.speed.Update(completed, every)
			t.upSpeed.Update(uploaded, every)
			t.st.Uploaded = t.upBase + uploaded
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
		t.st.Status = as
	}
	t.mu.Unlock()
	t.changed()
}

func (t *Torrent) dropLocked() {
	t.cancel()
	if t.t != nil {
		t.t.Drop()
		t.stor.Close()
	}
	t.t, t.stor, t.active, t.slot = nil, nil, false, false
	t.speed, t.upSpeed = rate.EWMA{}, rate.EWMA{}
}

func (t *Torrent) changed() {
	if f := t.s.cfg.Changed; f != nil {
		f(t)
	}
}
