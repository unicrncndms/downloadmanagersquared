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
	Kind     string                 `json:"kind,omitempty"` // KindTorrent; empty for HTTP
	ID       string                 `json:"id"`
	URL      string                 `json:"url"`
	PageURL  string                 `json:"pageUrl,omitempty"`
	Filename string                 `json:"filename"`
	Dir      string                 `json:"dir"`
	Size     int64                  `json:"size"`
	Done     int64                  `json:"done"`
	Status   Status                 `json:"status"`
	Error    string                 `json:"error,omitempty"`
	Speed    float64                `json:"speed"`
	ETA      float64                `json:"eta"`             // seconds, -1 when unknown
	Limit    int64                  `json:"limit,omitempty"` // HTTP only: the download's speed cap, bytes per second
	RangeOK  bool                   `json:"rangeOk"`
	Probed   bool                   `json:"probed"`
	SHA256   string                 `json:"sha256,omitempty"`
	MD5      string                 `json:"md5,omitempty"`
	Links    map[string]LinkSummary `json:"links"`
	Map      string                 `json:"map,omitempty"`   // HTTP, unfinished: the file as squares (fileMap)
	MapBy    []string               `json:"mapBy,omitempty"` // the interfaces Map's digits stand for
	Added    time.Time              `json:"added"`
	Finished time.Time              `json:"finished,omitzero"`

	VPN paths.Mode `json:"vpn,omitempty"`

	Folder *FolderSummary `json:"folder,omitempty"` // Download as folder, once asked for

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

	hashResult string // State.HashResult, for the label and note
	serverSHA  bool   // no checksum pasted: the server's SHA-256 is the one checked
	serverMD   bool   // and its MD5
	checking   bool   // a finished file is being checked against new checksums

	// Filled in by present.
	Label       string  `json:"label"`
	Tone        string  `json:"tone,omitempty"`
	Note        string  `json:"note,omitempty"` // after the progress numbers, or alone when there are none
	Actions     Actions `json:"actions"`
	PartialFile bool    `json:"partialFile"` // Remove asks whether to delete it
}

// FileSummary is one file of a torrent.
type FileSummary struct {
	Path     string `json:"path"` // within the torrent, "/"-separated
	Size     int64  `json:"size"`
	Selected bool   `json:"selected"`
}

func (d *Download) Summary() Summary {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := Summary{
		ID: d.st.ID, URL: d.st.URL, PageURL: d.st.PageURL, Filename: d.st.Filename, Dir: d.st.Dir,
		Size: d.st.Size, Done: d.doneLocked(), Status: d.st.Status, Error: d.st.Error,
		Speed: d.speed, ETA: -1, RangeOK: d.st.RangeOK, Probed: d.st.Probed,
		SHA256: d.st.SHA256, MD5: d.st.MD5, Links: map[string]LinkSummary{},
		hashResult: d.st.HashResult, checking: d.checking,
		serverSHA: d.st.SHA256 == "" && d.st.MD5 == "" && d.st.Version.SHA256 != "",
		serverMD:  d.st.SHA256 == "" && d.st.MD5 == "" && d.st.Version.MD5 != "",
		Added:     d.st.Added, Finished: d.st.Finished, VPN: d.st.VPN,
		Folder: d.folderSummaryLocked(), Limit: d.st.Limit,
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
		s.Map, s.MapBy = fileMap(s.Size, d.plan.ranges(), d.plan.fetching())
	case d.plan == nil && s.RangeOK:
		s.Map, s.MapBy = fileMap(s.Size, d.st.Remaining, nil)
	}
	now := time.Now()
	for id, l := range d.st.Links {
		ls := LinkSummary{Status: l.Status, Error: l.Error, Resolved: l.URL != d.st.URL && d.mirrorLocked(l.URL) == nil, Family: l.Family}
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
