package torrent

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	atorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/storage"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/rate"
)

// StatusMetadata is a magnet link's first step: learning the torrent's files from peers.
const StatusMetadata download.Status = "metadata"

// State is everything needed to resume a torrent after a restart.
type State struct {
	ID       string          `json:"id"`
	Magnet   string          `json:"magnet"`
	Dir      string          `json:"dir"`
	Name     string          `json:"name,omitempty"`
	Size     int64           `json:"size"`
	Status   download.Status `json:"status"`
	Error    string          `json:"error,omitempty"`
	Added    time.Time       `json:"added"`
	Finished time.Time       `json:"finished,omitzero"`
}

type Torrent struct {
	s *Session

	mu      sync.Mutex
	st      State
	t       *atorrent.Torrent // nil unless running
	stor    storage.ClientImplCloser
	cancel  context.CancelFunc
	running bool
	speed   rate.EWMA
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
	}
	switch {
	case t.st.Status == download.StatusCompleted:
		s.Done = t.st.Size
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

func (t *Torrent) Running() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running
}

func (t *Torrent) State() State {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.st
}

// Start joins the swarm and downloads every file.
func (t *Torrent) Start() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running || t.st.Status == download.StatusCompleted {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel, t.running = cancel, true
	// Only a magnet link is kept, so the files are learned from peers again each start.
	t.st.Status, t.st.Error = StatusMetadata, ""
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
	t.stor = storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir: t.st.Dir, PieceCompletion: storage.NewMapPieceCompletion(),
	})
	spec.Storage = t.stor
	at, _, err := t.s.cl.AddTorrentSpec(spec)
	if err != nil {
		t.stor.Close()
		t.stor = nil
		return nil, err
	}
	t.t = at
	return at, nil
}

func (t *Torrent) run(ctx context.Context) {
	at, err := t.join(ctx)
	if err != nil {
		t.mu.Lock()
		if ctx.Err() == nil {
			t.cancel()
			t.running = false
			t.failLocked(err)
		}
		t.mu.Unlock()
		t.changed()
		return
	}
	select {
	case <-at.GotInfo():
	case <-ctx.Done():
		return
	}
	t.mu.Lock()
	if t.t != at {
		t.mu.Unlock()
		return // stopped while the metadata arrived
	}
	// Peers choose the name; never let it be a path.
	t.st.Name, t.st.Size = download.SanitizeFilename(at.Name()), at.Length()
	t.st.Status = download.StatusRunning
	t.mu.Unlock()
	t.changed()
	at.DownloadAll()
	const every = 500 * time.Millisecond
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		done := at.BytesCompleted()
		t.mu.Lock()
		t.speed.Update(done, every)
		t.mu.Unlock()
		if done < at.Length() {
			t.changed()
			continue
		}
		t.mu.Lock()
		if t.t != at {
			t.mu.Unlock()
			return
		}
		t.dropLocked()
		t.st.Status, t.st.Finished = download.StatusCompleted, time.Now()
		t.mu.Unlock()
		t.changed()
		if f := t.s.cfg.Finished; f != nil {
			f(t)
		}
		return
	}
}

// Stop leaves the swarm; as is the status to show afterwards (paused or queued).
func (t *Torrent) Stop(as download.Status) {
	t.mu.Lock()
	if !t.running {
		// Already stopped: only the status changes, which is how resume re-queues.
		if t.st.Status != download.StatusCompleted {
			t.st.Status = as
		}
		t.mu.Unlock()
		t.changed()
		return
	}
	t.dropLocked()
	t.st.Status = as
	t.mu.Unlock()
	t.changed()
}

func (t *Torrent) dropLocked() {
	t.cancel()
	if t.t != nil {
		t.t.Drop()
		t.stor.Close()
	}
	t.t, t.stor, t.running = nil, nil, false
}

func (t *Torrent) failLocked(err error) {
	t.st.Status, t.st.Error = download.StatusFailed, err.Error()
}

func (t *Torrent) changed() {
	if f := t.s.cfg.Changed; f != nil {
		f(t)
	}
}
