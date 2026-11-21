package download

import (
	"slices"
	"time"

	"downloadmanagersquared/engine/internal/archive"
	"downloadmanagersquared/engine/internal/paths"
)

type LinkSummary struct {
	Status   LinkStatus `json:"status"`
	Error    string     `json:"error,omitempty"`
	Speed    float64    `json:"speed"`
	Conns    int        `json:"conns"`
	Resolved bool       `json:"resolved"`         // fetched by the browser for this interface
	Mirror   string     `json:"mirror,omitempty"` // the host of the mirror it uses, when the file has several
	Family   string     `json:"family,omitempty"` // "IPv4" or "IPv6", on an interface with IPv6
	Up       float64    `json:"up,omitempty"`     // torrents: upload speed to the peers on this path
	Bytes    int64      `json:"bytes,omitempty"`  // HTTP: what this interface fetched of the file, for each one's share on a finished download

	// Filled in by present.
	Label    string `json:"label"`
	Tone     string `json:"tone,omitempty"`
	CanRetry bool   `json:"canRetry,omitempty"`

	// Backoff, only for the label.
	allowed  int  // connections backoff allows right now
	maxConns int  // connections the settings allow
	busy     bool // holding back because the server pushed back
}

// Summary is what the UI shows for a download.
type Summary struct {
	Kind     string  `json:"kind,omitempty"` // KindTorrent; empty for HTTP
	ID       string  `json:"id"`
	URL      string  `json:"url"`
	PageURL  string  `json:"pageUrl,omitempty"`
	Filename string  `json:"filename"`
	Dir      string  `json:"dir"`
	Size     int64   `json:"size"`
	Done     int64   `json:"done"`
	Status   Status  `json:"status"`
	Error    string  `json:"error,omitempty"`
	Speed    float64 `json:"speed"`
	ETA      float64 `json:"eta"`             // seconds, -1 when unknown
	Limit    int64   `json:"limit,omitempty"` // HTTP only: the download's speed cap, bytes per second
	// LimitedBy names the speed limit holding a running download back ("Wi-Fi's limit"), LimitedTo its size in bytes per second; set by the engine.
	LimitedBy string                 `json:"limitedBy,omitempty"`
	LimitedTo int64                  `json:"limitedTo,omitempty"`
	RangeOK   bool                   `json:"rangeOk"`
	Probed    bool                   `json:"probed"`
	SHA256    string                 `json:"sha256,omitempty"`
	MD5       string                 `json:"md5,omitempty"`
	Links     map[string]LinkSummary `json:"links"`
	Map       string                 `json:"map,omitempty"`     // HTTP, unfinished: the file as squares (fileMap)
	MapBy     []string               `json:"mapBy,omitempty"`   // the interfaces Map's digits stand for
	MapFill   string                 `json:"mapFill,omitempty"` // how far each square Map's digits mark is (mapFill)
	MapPart   string                 `json:"mapPart,omitempty"` // how much of each square is on disk, two characters each (partMap)
	Added     time.Time              `json:"added"`
	Finished  time.Time              `json:"finished,omitzero"`
	Active    float64                `json:"active,omitempty"` // seconds it ran, not counting the time queued or paused

	VPN paths.Mode `json:"vpn,omitempty"`

	Folder *FolderSummary `json:"folder,omitempty"` // Download as folder, once asked for

	HostKey *HostKeyAsk `json:"hostKey,omitempty"` // SFTP: the server's key waits for Trust

	Mirrors   []Mirror `json:"mirrors,omitempty"`   // the file's sources, when it has several
	Group     string   `json:"group,omitempty"`     // the files of one Metalink share it, and the parts of a split archive
	GroupName string   `json:"groupName,omitempty"` // the Metalink's name, or the split archive's

	// Torrents only: the files to choose from (while choosing), then the upload once complete.
	// Files stays out of the state sent every second: a torrent of many files
	// would make it bigger than Firefox takes. The picker asks for it ("torrentFiles").
	Files     []FileSummary `json:"-"`
	FileCount int           `json:"fileCount,omitempty"`
	FilesSize int64         `json:"filesSize,omitempty"`
	AskVPN    bool          `json:"askVpn,omitempty"` // waiting for: VPN, or the current connection?
	Peers     int           `json:"peers,omitempty"`  // connected
	Seeds     int           `json:"seeds,omitempty"`  // connected peers with the whole torrent
	Swarm     int           `json:"swarm,omitempty"`  // peers known, connected or not
	InfoHash  string        `json:"infoHash,omitempty"`
	Uploaded  int64         `json:"uploaded,omitempty"`
	Ratio     float64       `json:"ratio,omitempty"`
	UpSpeed   float64       `json:"upSpeed,omitempty"`
	InSwarm   bool          `json:"inSwarm,omitempty"` // exchanging pieces with peers right now

	WaitVPN bool `json:"-"` // paused only because no tunnel is up: it resumes by itself, so for the section it needs nobody
	// WaitDrive: paused only because its drive is gone; it resumes by itself too.
	WaitDrive bool `json:"-"`

	hashResult string // State.HashResult, for the label and note
	serverSHA  bool   // no checksum pasted: the server's SHA-256 is the one checked
	serverMD   bool   // and its MD5
	checking   bool   // a finished file is being checked against new checksums

	// Filled in by present.
	Label       string  `json:"label"`
	Tone        string  `json:"tone,omitempty"`
	Note        string  `json:"note,omitempty"` // after the progress numbers, or alone when there are none
	Actions     Actions `json:"actions"`
	PartialFile bool    `json:"partialFile"`     // Remove asks whether to delete it
	Section     string  `json:"section"`         // which section of the list it is in: needs, active, seeding, queued, paused, done
	Plain       bool    `json:"plain,omitempty"` // the label says only what the status does, so a UI may leave it off
}

// FileSummary is one file of a torrent.
type FileSummary struct {
	Path     string `json:"path"` // within the torrent, "/"-separated
	Size     int64  `json:"size"`
	Selected bool   `json:"selected"`
	Done     int64  `json:"done,omitempty"` // bytes on disk, while the torrent is in the swarm
}

// sumKey is all a finished download's Summary reads besides its State, which
// changed() versions (rev): what can change without a save.
type sumKey struct {
	rev      uint64
	view     *SetSummary
	keep     string // the archiveKeeping setting
	checking bool
	speed    float64
}

// sumKeyLocked says what the cached Summary stands for; ok is false while
// the download can still change in ways the key does not cover (running,
// extracting, moving), so no Summary is cached then.
func (d *Download) sumKeyLocked() (k sumKey, ok bool) {
	if d.st.Status != StatusCompleted || d.running || d.plan != nil || d.fold != nil || d.parked != nil || d.moving {
		return k, false
	}
	k = sumKey{rev: d.rev.Load(), keep: d.keepDefault(), checking: d.checking, speed: d.speed}
	if d.set != nil {
		k.view = d.set.Summary()
	}
	return k, true
}

// changed tells the engine d's State changed and drops the cached Summary.
func (d *Download) changed() {
	d.rev.Add(1)
	d.env.Changed(d)
}

// Summary is what the UI shows. A finished download's is kept until
// something it reads changes: the list is summarised every second. The
// copy shares its maps and slices with the kept one: do not write to them.
func (d *Download) Summary() Summary {
	d.mu.Lock()
	defer d.mu.Unlock()
	k, cacheable := d.sumKeyLocked()
	if cacheable && d.sumOK && d.sumKey == k && d.seen != nil {
		return d.sum // observeLocked would find nothing new: the same summary was the last it saw
	}
	s := d.summaryLocked()
	if cacheable {
		d.sum, d.sumKey, d.sumOK = s, k, true
	}
	return s
}

func (d *Download) summaryLocked() Summary {
	s := Summary{
		ID: d.st.ID, URL: d.st.URL, PageURL: d.st.PageURL, Filename: d.st.Filename, Dir: d.st.Dir,
		Size: d.st.Size, Done: d.doneLocked(), Status: d.st.Status, Error: d.st.Error,
		Speed: d.speed, ETA: -1, RangeOK: d.st.RangeOK, Probed: d.st.Probed,
		SHA256: d.st.SHA256, MD5: d.st.MD5, Links: map[string]LinkSummary{},
		hashResult: d.st.HashResult, checking: d.checking,
		serverSHA: d.st.SHA256 == "" && d.st.MD5 == "" && d.st.Version.SHA256 != "",
		serverMD:  d.st.SHA256 == "" && d.st.MD5 == "" && d.st.Version.MD5 != "",
		Added:     d.st.Added, Finished: d.st.Finished, Active: d.st.Active, VPN: d.st.VPN,
		HostKey: d.st.HostAsk, Folder: d.folderSummaryLocked(), Limit: d.st.Limit, WaitVPN: d.st.WaitVPN, WaitDrive: d.st.WaitDrive != "",
		Mirrors: slices.Clone(d.st.Mirrors), Group: d.st.Group, GroupName: d.st.GroupName,
	}
	if s.Group == "" && s.Filename != "" {
		// The parts of a split archive show as one group, however they were added.
		if set, _, ok := archive.PartOf(s.Filename); ok {
			s.Group, s.GroupName = archive.SetKey(d.setDirLocked(), set), set
		}
	}
	if d.running {
		s.ETA = d.eta.Seconds()
	}
	switch {
	case s.Status == StatusCompleted || !d.st.Allocated:
	case d.plan != nil && d.plan.splittable:
		at := d.plan.fetching()
		s.Map, s.MapBy = fileMap(s.Size, d.plan.ranges(), at, d.plan.landed, d.plan.writers)
		s.MapFill = mapFill(s.Size, at)
		s.MapPart, s.MapBy = partMap(s.Size, d.plan.ranges(), d.plan.landed, d.plan.writers, s.MapBy)
	case d.plan == nil && s.RangeOK:
		s.Map, s.MapBy = fileMap(s.Size, d.st.Remaining, nil, nil, nil)
		s.MapPart, s.MapBy = partMap(s.Size, d.st.Remaining, nil, nil, s.MapBy)
	}
	now := time.Now()
	for id, l := range d.st.Links {
		ls := LinkSummary{Status: l.Status, Error: l.Error, Resolved: l.URL != d.st.URL && d.mirrorLocked(l.URL) == nil, Family: l.Family, Bytes: l.Bytes}
		if len(d.st.Mirrors) > 0 {
			ls.Mirror = hostOf(l.URL)
		}
		if ir := d.ifs[id]; ir != nil && d.running {
			ls.Speed, ls.Conns = ir.ewma.Value(), ir.active
			ls.allowed, ls.maxConns, ls.busy = ir.backoff.target(), ir.backoff.conns, ir.backoff.busy(now)
		}
		s.Links[id] = ls
	}
	s = Present(s)
	d.observeLocked(s, now)
	return s
}
