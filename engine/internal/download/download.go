// Package download fetches one file over several interfaces at once. Each
// interface has its own link to the file and runs several range requests; idle
// connections take over the far end of the slowest connection's chunk.
package download

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"downloadmanagersquared/engine/internal/rate"
	"downloadmanagersquared/engine/internal/winutil"
)

const (
	minSplit     = 4 << 20   // never create a chunk smaller than this by splitting
	bufSize      = 256 << 10 // read/write unit per connection
	stallTimeout = 30 * time.Second
	maxResolves  = 3
	diskReserve  = 256 << 20 // pause when free space falls below this
	partSuffix   = ".dms.part"
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusPaused    Status = "paused"
	StatusVerifying Status = "verifying"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
)

type LinkStatus string

const (
	LinkPending   LinkStatus = "pending" // has a URL that still needs probing
	LinkProbing   LinkStatus = "probing"
	LinkReady     LinkStatus = "ready"
	LinkResolving LinkStatus = "resolving" // waiting for the browser to fetch a link via this interface
	LinkFailed    LinkStatus = "failed"
	LinkMismatch  LinkStatus = "mismatch" // serves a different file; excluded from this download
)

// Link is one interface's way to the file.
type Link struct {
	URL      string     `json:"url,omitempty"`
	Cookie   string     `json:"cookie,omitempty"`
	Referer  string     `json:"referer,omitempty"`
	Status   LinkStatus `json:"status"`
	Error    string     `json:"error,omitempty"`
	Resolves int        `json:"resolves,omitempty"`
	// NeedsPage: this connection needs its own link but no landing page is known yet.
	NeedsPage bool `json:"needsPage,omitempty"`
	retryAt   time.Time
	pageHit   bool // got an HTML page for the original link; meaning decided once all connections answered
}

type Range struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"` // exclusive
}

// State is everything needed to resume a download after a restart.
type State struct {
	ID         string           `json:"id"`
	URL        string           `json:"url"`
	PageURL    string           `json:"pageUrl,omitempty"`
	Cookie     string           `json:"cookie,omitempty"`
	Referer    string           `json:"referer,omitempty"`
	UserAgent  string           `json:"userAgent,omitempty"`
	Dir        string           `json:"dir"`
	Filename   string           `json:"filename"`
	RemoteName string           `json:"remoteName,omitempty"` // name the server reports; must match across links
	Size       int64            `json:"size"`
	RangeOK    bool             `json:"rangeOk"`
	Probed     bool             `json:"probed"`
	Allocated  bool             `json:"allocated"`
	SHA256     string           `json:"sha256,omitempty"`
	MD5        string           `json:"md5,omitempty"`
	Status     Status           `json:"status"`
	Error      string           `json:"error,omitempty"`
	Links      map[string]*Link `json:"links"`
	Remaining  []Range          `json:"remaining,omitempty"`
	Added      time.Time        `json:"added"`
	Finished   time.Time        `json:"finished,omitzero"`
}

// Iface is an interface the engine currently allows downloads to use.
type Iface struct {
	ID     string
	Client *http.Client
	Conns  int
}

type Env interface {
	Usable() []Iface
	MainID() string
	ETAWindow() time.Duration
	RequestResolve(id, ifaceID, pageURL string)
	Changed(d *Download)
	Finished(d *Download)
}

type segment struct {
	pos     int64 // next byte to fetch (claimed)
	written int64 // bytes before this are on disk
	end     int64
	owner   *worker
}

type ifaceRun struct {
	conns        int
	throttle     int // connections removed after the server pushed back
	active       int
	pausedUntil  time.Time
	lastThrottle time.Time
	netFails     int
	bytes        atomic.Int64
	ewma         rate.EWMA
}

func (ir *ifaceRun) target() int { return max(1, ir.conns-ir.throttle) }

type worker struct {
	d      *Download
	iface  string
	client *http.Client
	link   Link
	ir     *ifaceRun
	cancel context.CancelFunc
	bytes  atomic.Int64
	ewma   rate.EWMA
	speed  float64
}

type pauseError struct{ msg string }

func (e *pauseError) Error() string { return e.msg }

type Download struct {
	env Env
	mu  sync.Mutex
	st  State

	running bool
	cancel  context.CancelFunc
	done    chan struct{}
	stopAs  Status
	fatal   chan error
	wg      sync.WaitGroup

	file       *os.File
	segs       []*segment
	streamDone bool
	streamPos  int64
	workers    map[*worker]struct{}
	ifs        map[string]*ifaceRun
	probing    map[string]bool

	meter    rate.Meter
	total    rate.EWMA
	speed    float64
	eta      float64
	etaAt    time.Time
	lastTick time.Time
	lastSave time.Time
	lastDisk time.Time
}

func New(env Env, st State) *Download {
	if st.Links == nil {
		st.Links = map[string]*Link{}
	}
	for _, l := range st.Links {
		if l.Status == LinkProbing {
			l.Status = LinkPending
		}
	}
	return &Download{env: env, st: st}
}

func (d *Download) ID() string { return d.st.ID }

func (d *Download) Status() Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.st.Status
}

func (d *Download) Running() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running
}

// State returns a copy suitable for saving.
func (d *Download) State() State {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.syncRemainingLocked()
	st := d.st
	st.Links = make(map[string]*Link, len(d.st.Links))
	for k, l := range d.st.Links {
		c := *l
		st.Links[k] = &c
	}
	st.Remaining = append([]Range(nil), d.st.Remaining...)
	return st
}

func (d *Download) syncRemainingLocked() {
	if d.segs == nil || !d.st.RangeOK {
		return
	}
	var rem []Range
	for _, s := range d.segs {
		if s.written < s.end {
			rem = append(rem, Range{s.written, s.end})
		}
	}
	d.st.Remaining = rem
}

// Start begins (or resumes) downloading.
func (d *Download) Start() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running || d.st.Status == StatusCompleted {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.running, d.cancel, d.done, d.stopAs = true, cancel, make(chan struct{}), ""
	d.fatal = make(chan error, 1)
	d.st.Status, d.st.Error = StatusRunning, ""
	d.workers, d.ifs, d.probing = map[*worker]struct{}{}, map[string]*ifaceRun{}, map[string]bool{}
	d.meter = rate.Meter{Window: d.env.ETAWindow()}
	d.total, d.speed, d.eta, d.etaAt = rate.EWMA{}, 0, -1, time.Time{}
	d.lastTick = time.Now()
	for _, l := range d.st.Links {
		if l.Status == LinkFailed && l.retryAt.IsZero() && strings.HasPrefix(l.Error, "unreachable") {
			l.Status = LinkPending
		}
	}
	go d.run(ctx)
}

// Stop halts a running download and leaves it in the given status (paused or queued).
func (d *Download) Stop(as Status) {
	d.mu.Lock()
	if !d.running {
		if d.st.Status == StatusQueued || d.st.Status == StatusPaused || d.st.Status == StatusFailed {
			d.st.Status = as
		}
		d.mu.Unlock()
		return
	}
	d.stopAs = as
	cancel, done := d.cancel, d.done
	d.mu.Unlock()
	cancel()
	<-done
}

// SetResolved stores a link the browser fetched through the given interface.
func (d *Download) SetResolved(ifaceID, url, cookie, referer string) {
	d.mu.Lock()
	l := d.st.Links[ifaceID]
	if l == nil {
		l = &Link{}
		d.st.Links[ifaceID] = l
	}
	l.URL, l.Cookie, l.Referer = url, cookie, referer
	l.Status, l.Error, l.retryAt = LinkPending, "", time.Time{}
	d.mu.Unlock()
	d.env.Changed(d)
}

func (d *Download) ResolveFailed(ifaceID, msg string) {
	d.mu.Lock()
	if l := d.st.Links[ifaceID]; l != nil && l.Status == LinkResolving {
		l.Status, l.Error, l.retryAt = LinkFailed, "browser: "+msg, time.Time{}
	}
	d.mu.Unlock()
	d.env.Changed(d)
}

// RetryLink gives a failed or mismatched interface another chance.
func (d *Download) RetryLink(ifaceID string) {
	d.mu.Lock()
	if l := d.st.Links[ifaceID]; l != nil && l.Status != LinkProbing {
		l.Status, l.Error, l.Resolves, l.retryAt = LinkPending, "", 0, time.Time{}
	}
	d.mu.Unlock()
	d.env.Changed(d)
}

func (d *Download) SetHashes(sha, md string) {
	d.mu.Lock()
	d.st.SHA256, d.st.MD5 = strings.ToLower(strings.TrimSpace(sha)), strings.ToLower(strings.TrimSpace(md))
	d.mu.Unlock()
	d.env.Changed(d)
}

// SetPageURL sets the landing page and retries connections that were waiting for one.
func (d *Download) SetPageURL(u string) {
	d.mu.Lock()
	d.st.PageURL = strings.TrimSpace(u)
	if d.st.PageURL != "" {
		for _, l := range d.st.Links {
			if l.NeedsPage && l.Status == LinkFailed {
				l.Status, l.Error, l.Resolves, l.NeedsPage = LinkPending, "", 0, false
			}
		}
	}
	d.mu.Unlock()
	d.env.Changed(d)
}

func (d *Download) partPath() string { return filepath.Join(d.st.Dir, d.st.Filename+partSuffix) }

// PartPath is where the unfinished data lives (or the final file once completed).
func (d *Download) Path() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.st.Status == StatusCompleted {
		return filepath.Join(d.st.Dir, d.st.Filename)
	}
	return d.partPath()
}

func (d *Download) run(ctx context.Context) {
	err := d.loop(ctx)
	d.mu.Lock()
	for w := range d.workers {
		w.cancel()
	}
	d.mu.Unlock()
	d.wg.Wait()

	if err == nil {
		err = d.complete(ctx)
	}

	d.mu.Lock()
	d.syncRemainingLocked()
	if d.file != nil {
		d.file.Close()
		d.file = nil
	}
	d.segs = nil
	var pe *pauseError
	finished := false
	switch {
	case d.stopAs != "":
		d.st.Status = d.stopAs
	case errors.As(err, &pe):
		d.st.Status, d.st.Error = StatusPaused, pe.msg
	case err != nil:
		d.st.Status, d.st.Error, finished = StatusFailed, err.Error(), true
	default:
		d.st.Status, d.st.Finished, d.st.Remaining, finished = StatusCompleted, time.Now(), nil, true
	}
	d.speed, d.eta = 0, -1
	d.running = false
	close(d.done)
	d.mu.Unlock()

	d.env.Changed(d)
	if finished {
		d.env.Finished(d)
	}
}

func (d *Download) loop(ctx context.Context) error {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		fin, err := d.tick(ctx)
		if err != nil || fin {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-d.fatal:
			return err
		case <-t.C:
		}
	}
}

func (d *Download) tick(ctx context.Context) (bool, error) {
	usable := d.env.Usable()
	mainID := d.env.MainID()
	now := time.Now()
	changed := false

	d.mu.Lock()
	defer func() {
		d.mu.Unlock()
		if changed {
			d.env.Changed(d)
		}
	}()

	byID := map[string]Iface{}
	for _, ifc := range usable {
		byID[ifc.ID] = ifc
		l := d.st.Links[ifc.ID]
		if l == nil {
			l = &Link{URL: d.st.URL, Cookie: d.st.Cookie, Referer: d.st.Referer, Status: LinkPending}
			d.st.Links[ifc.ID] = l
			changed = true
		}
		if l.Status == LinkFailed && !l.retryAt.IsZero() && now.After(l.retryAt) {
			l.Status, l.Error, l.retryAt = LinkPending, "", time.Time{}
		}
		if l.Status != LinkPending || d.probing[ifc.ID] {
			continue
		}
		// Without range support only the main connection is used, so don't touch the others.
		if d.st.Probed && !d.st.RangeOK && ifc.ID != mainID {
			continue
		}
		d.probing[ifc.ID] = true
		l.Status = LinkProbing
		go d.probe(ctx, ifc, *l)
	}
	d.decidePageHitsLocked(byID)

	if !d.st.Probed {
		return false, nil
	}
	if d.file == nil {
		if err := d.openFileLocked(); err != nil {
			return false, err
		}
		changed = true
	}

	for w := range d.workers {
		l := d.st.Links[w.iface]
		_, ok := byID[w.iface]
		if !ok || l == nil || l.Status != LinkReady || l.URL != w.link.URL {
			w.cancel()
		}
	}

	if d.st.RangeOK {
		for _, ifc := range usable {
			l := d.st.Links[ifc.ID]
			if l.Status != LinkReady {
				continue
			}
			ir := d.ifRun(ifc.ID)
			ir.conns = ifc.Conns
			if ir.throttle > 0 && now.Sub(ir.lastThrottle) > 2*time.Minute {
				ir.throttle--
				ir.lastThrottle = now
			}
			if now.Before(ir.pausedUntil) {
				continue
			}
			for ir.active < ir.target() {
				seg := d.claimLocked(ifc.ID)
				if seg == nil {
					break
				}
				d.spawnLocked(ctx, ifc, *l, seg)
			}
		}
	} else if len(d.workers) == 0 && !d.streamDone {
		if ifc, ok := byID[mainID]; ok && d.st.Links[mainID].Status == LinkReady {
			d.spawnStreamLocked(ctx, ifc, *d.st.Links[mainID])
		}
	}

	d.statsLocked(now)

	if now.Sub(d.lastSave) >= 5*time.Second {
		d.lastSave = now
		changed = true
	}
	if now.Sub(d.lastDisk) >= 30*time.Second {
		d.lastDisk = now
		if free, err := winutil.FreeSpace(d.st.Dir); err == nil && free < diskReserve && d.remainingLocked() > 0 {
			return false, &pauseError{fmt.Sprintf("Paused: disk almost full (%s free)", human(free))}
		}
	}

	if d.st.RangeOK {
		return d.remainingLocked() == 0 && len(d.workers) == 0, nil
	}
	return d.streamDone && len(d.workers) == 0, nil
}

func (d *Download) ifRun(id string) *ifaceRun {
	ir := d.ifs[id]
	if ir == nil {
		ir = &ifaceRun{}
		d.ifs[id] = ir
	}
	return ir
}

func (d *Download) probe(ctx context.Context, ifc Iface, l Link) {
	res, err := probeLink(ctx, ifc.Client, l, d.st.UserAgent)
	d.mu.Lock()
	delete(d.probing, ifc.ID)
	cur := d.st.Links[ifc.ID]
	if cur == nil || cur.URL != l.URL || cur.Status != LinkProbing {
		d.mu.Unlock()
		return
	}
	if ctx.Err() != nil {
		cur.Status = LinkPending
		d.mu.Unlock()
		return
	}
	d.applyProbeLocked(ifc.ID, cur, res, err)
	d.mu.Unlock()
	d.env.Changed(d)
}

func (d *Download) applyProbeLocked(ifaceID string, l *Link, r probeResult, err error) {
	if err != nil {
		log.Printf("download %s probe via %s: %s", d.st.ID, ifaceID, shortErr(err))
	} else {
		log.Printf("download %s probe via %s: HTTP %d, size %d, ranges %v, page %v", d.st.ID, ifaceID, r.status, r.size, r.rangeOK, r.isPage)
	}
	if err != nil {
		l.Status, l.Error, l.retryAt = LinkFailed, "unreachable: "+shortErr(err), time.Now().Add(30*time.Second)
		return
	}
	if r.rejected() {
		if r.isPage && d.st.PageURL == "" && l.URL == d.st.URL {
			// Either the link is a landing page, or it is the file link and this
			// connection got an error page for it. Decide once every connection has answered.
			l.Status, l.Error, l.pageHit = LinkFailed, "got a web page instead of the file", true
			return
		}
		reason := fmt.Sprintf("link not valid on this connection (HTTP %d)", r.status)
		if r.isPage {
			reason = "link leads to a web page, not the file"
		}
		d.needResolveLocked(ifaceID, l, reason)
		return
	}
	if r.status >= 400 {
		l.Status, l.Error, l.retryAt = LinkFailed, fmt.Sprintf("HTTP %d", r.status), time.Now().Add(60*time.Second)
		return
	}
	if !d.st.Probed {
		d.st.Size, d.st.RangeOK, d.st.RemoteName, d.st.Probed = r.size, r.rangeOK && r.size > 0, r.name, true
		if d.st.Filename == "" {
			d.st.Filename = r.name
		}
		if d.st.Filename == "" {
			d.st.Filename = "download"
		}
	} else {
		if r.size != d.st.Size {
			l.Status, l.Error = LinkMismatch, fmt.Sprintf("different file size (%d vs %d bytes)", r.size, d.st.Size)
			return
		}
		if d.st.RemoteName != "" && r.name != "" && !strings.EqualFold(r.name, d.st.RemoteName) {
			l.Status, l.Error = LinkMismatch, fmt.Sprintf("different file name (%q vs %q)", r.name, d.st.RemoteName)
			return
		}
		if d.st.RangeOK && !r.rangeOK {
			l.Status, l.Error = LinkFailed, "this link does not allow split downloading"
			return
		}
	}
	l.Status, l.Error, l.Resolves, l.retryAt = LinkReady, "", 0, time.Time{}
}

// decidePageHitsLocked settles connections that got a web page for the original link.
// If another connection got the file, the link is the file and those connections
// need their own link from the landing page. Only if no connection got the file is
// the link itself the landing page.
func (d *Download) decidePageHitsLocked(usable map[string]Iface) {
	var hits []string
	for id, l := range d.st.Links {
		if l.pageHit {
			hits = append(hits, id)
		}
	}
	if len(hits) == 0 || len(d.probing) > 0 {
		return
	}
	if !d.st.Probed && d.st.PageURL == "" {
		for id := range usable {
			if l := d.st.Links[id]; l == nil || !l.pageHit && l.Status != LinkFailed {
				return // still waiting on a connection that might get the file
			}
		}
		d.st.PageURL = d.st.URL
	}
	for _, id := range hits {
		l := d.st.Links[id]
		l.pageHit = false
		d.needResolveLocked(id, l, "this connection gets a web page instead of the file")
	}
}

func (d *Download) needResolveLocked(ifaceID string, l *Link, reason string) {
	if d.st.PageURL == "" {
		l.Status, l.NeedsPage = LinkFailed, true
		l.Error = reason + ". It needs its own link, but the page the download came from is unknown: enter it under Details as the landing page"
		return
	}
	if l.Resolves >= maxResolves {
		l.Status, l.Error = LinkFailed, fmt.Sprintf("%s; gave up after %d new links", reason, maxResolves)
		return
	}
	l.Resolves++
	l.Status, l.Error, l.NeedsPage = LinkResolving, reason, false
	id, page := d.st.ID, d.st.PageURL
	go d.env.RequestResolve(id, ifaceID, page)
}

func (d *Download) openFileLocked() error {
	if err := os.MkdirAll(d.st.Dir, 0o755); err != nil {
		return err
	}
	if !d.st.Allocated {
		d.st.Filename = uniqueName(d.st.Dir, d.st.Filename)
	}
	path := d.partPath()

	if !d.st.RangeOK {
		// No resume without ranges: every attempt starts from byte 0.
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if d.st.Size > 0 {
			if err := checkSpace(d.st.Dir, d.st.Size); err != nil {
				f.Close()
				return err
			}
		}
		d.file, d.st.Allocated, d.streamDone, d.streamPos = f, true, false, 0
		return nil
	}

	if !d.st.Allocated {
		if err := checkSpace(d.st.Dir, d.st.Size); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		// Sparse: setting the length is instant and nothing gets zero-filled.
		winutil.SetSparse(f, true)
		if err := f.Truncate(d.st.Size); err != nil {
			f.Close()
			return err
		}
		d.file, d.st.Allocated = f, true
		d.st.Remaining = []Range{{0, d.st.Size}}
	} else {
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return fmt.Errorf("partial file is gone: %w", err)
		}
		if fi, err := f.Stat(); err != nil || fi.Size() != d.st.Size {
			f.Close()
			return fmt.Errorf("partial file %s has the wrong size", path)
		}
		var rem int64
		for _, r := range d.st.Remaining {
			rem += r.End - r.Start
		}
		if err := checkSpace(d.st.Dir, rem); err != nil {
			f.Close()
			return err
		}
		d.file = f
	}
	d.segs = nil
	for _, r := range d.st.Remaining {
		d.segs = append(d.segs, &segment{pos: r.Start, written: r.Start, end: r.End})
	}
	return nil
}

func checkSpace(dir string, need int64) error {
	free, err := winutil.FreeSpace(dir)
	if err != nil {
		return nil
	}
	if free < need+diskReserve {
		return &pauseError{fmt.Sprintf("Paused: not enough disk space (need %s, %s free)", human(need), human(free))}
	}
	return nil
}

// claimLocked hands out work: a free chunk if there is one, otherwise the far
// end of the chunk that would take longest to finish.
func (d *Download) claimLocked(ifaceID string) *segment {
	var free *segment
	for _, s := range d.segs {
		if s.owner == nil && s.written < s.end && (free == nil || s.written < free.written) {
			free = s
		}
	}
	if free != nil {
		free.pos = free.written
		return free
	}

	var victim *segment
	var worst float64
	for _, s := range d.segs {
		if s.owner == nil || s.end-s.pos < 2*minSplit {
			continue
		}
		if t := float64(s.end-s.pos) / math.Max(s.owner.speed, 1); t > worst {
			worst, victim = t, s
		}
	}
	if victim == nil || worst < 3 {
		return nil
	}
	vs := math.Max(victim.owner.speed, 1)
	vn := vs
	if ir := d.ifs[ifaceID]; ir != nil && ir.active > 0 && ir.ewma.Value() > 0 {
		vn = ir.ewma.Value() / float64(ir.active)
	}
	// Split so both halves should finish at about the same time.
	cut := victim.pos + int64(float64(victim.end-victim.pos)*vs/(vs+vn))
	cut = min(max(cut, victim.pos+minSplit), victim.end-minSplit)
	ns := &segment{pos: cut, written: cut, end: victim.end}
	victim.end = cut
	d.segs = append(d.segs, ns)
	return ns
}

func (d *Download) remainingLocked() int64 {
	if d.segs == nil {
		var n int64
		for _, r := range d.st.Remaining {
			n += r.End - r.Start
		}
		return n
	}
	var n int64
	for _, s := range d.segs {
		n += s.end - s.written
	}
	return n
}

func (d *Download) doneLocked() int64 {
	switch {
	case d.st.Status == StatusCompleted:
		return d.st.Size
	case !d.st.Allocated:
		return 0
	case !d.st.RangeOK:
		return d.streamPos
	}
	return d.st.Size - d.remainingLocked()
}

func (d *Download) spawnLocked(ctx context.Context, ifc Iface, l Link, seg *segment) {
	wctx, cancel := context.WithCancel(ctx)
	w := &worker{d: d, iface: ifc.ID, client: ifc.Client, link: l, ir: d.ifRun(ifc.ID), cancel: cancel}
	seg.owner = w
	d.workers[w] = struct{}{}
	d.ifRun(ifc.ID).active++
	d.wg.Add(1)
	go w.run(wctx, seg)
}

// nextLocked lets a worker that finished a chunk keep going without waiting for a tick.
// A backoff pause only stops new connections; working ones carry on.
func (d *Download) nextLocked(w *worker) *segment {
	l := d.st.Links[w.iface]
	ir := d.ifRun(w.iface)
	if l == nil || l.Status != LinkReady || l.URL != w.link.URL || ir.active > ir.target() {
		return nil
	}
	return d.claimLocked(w.iface)
}

func (d *Download) workerFailedLocked(w *worker, err error) {
	now := time.Now()
	ir := d.ifRun(w.iface)
	l := d.st.Links[w.iface]
	var fe *fetchError
	if !errors.As(err, &fe) {
		fe = &fetchError{kind: kindNet, msg: shortErr(err)}
	}
	log.Printf("download %s via %s: %s", d.st.ID, w.iface, fe.msg)
	// A rejection while sibling connections on the same link still work is a
	// per-IP connection limit, not an expired link: back off instead.
	if fe.kind == kindExpired && ir.active > 1 {
		fe = &fetchError{kind: kindThrottle, msg: fe.msg, retryAfter: 15 * time.Second}
	}
	switch fe.kind {
	case kindExpired:
		if l != nil && l.URL == w.link.URL && l.Status == LinkReady {
			l.Status, l.Error = LinkPending, fe.msg
		}
	case kindThrottle:
		ir.throttle = min(ir.throttle+1, max(ir.conns-1, 0))
		ir.lastThrottle, ir.pausedUntil = now, now.Add(fe.retryAfter)
	case kindRange:
		if l != nil && l.URL == w.link.URL {
			l.Status, l.Error = LinkFailed, fe.msg
		}
	case kindDisk:
		select {
		case d.fatal <- errors.New("writing to disk failed: " + fe.msg):
		default:
		}
	default:
		ir.netFails++
		ir.pausedUntil = now.Add(2 * time.Second)
		if ir.netFails >= 3 && l != nil && l.Status == LinkReady {
			ir.netFails = 0
			l.Status, l.Error, l.retryAt = LinkFailed, "unreachable: "+fe.msg, now.Add(30*time.Second)
		}
	}
}

func (w *worker) run(ctx context.Context, seg *segment) {
	d := w.d
	defer d.wg.Done()
	defer w.cancel()
	buf := make([]byte, bufSize)
	for seg != nil {
		err := w.fetch(ctx, seg, buf)
		d.mu.Lock()
		seg.owner = nil
		seg.pos = seg.written
		switch {
		case ctx.Err() != nil:
			seg = nil
		case err != nil:
			d.workerFailedLocked(w, err)
			seg = nil
		default:
			d.ifRun(w.iface).netFails = 0
			if seg = d.nextLocked(w); seg != nil {
				seg.owner = w
			}
		}
		if seg == nil {
			delete(d.workers, w)
			d.ifRun(w.iface).active--
		}
		d.mu.Unlock()
	}
}

func (w *worker) fetch(ctx context.Context, seg *segment, buf []byte) error {
	d := w.d
	d.mu.Lock()
	start, end := seg.pos, seg.end
	d.mu.Unlock()
	if start >= end {
		return nil
	}

	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stall := time.AfterFunc(stallTimeout, cancel)
	defer stall.Stop()

	req, err := newRequest(rctx, w.link, d.st.UserAgent)
	if err != nil {
		return &fetchError{kind: kindRange, msg: err.Error()}
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end-1))
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if fe := statusError(resp); fe != nil {
		return fe
	}
	if resp.StatusCode != http.StatusPartialContent {
		return &fetchError{kind: kindRange, msg: "server ignored the range request (" + resp.Status + ")"}
	}
	if cs, _, ok := parseContentRange(resp.Header.Get("Content-Range")); !ok || cs != start {
		return &fetchError{kind: kindRange, msg: "server sent the wrong part of the file"}
	}

	ir := w.ir
	for {
		n, rerr := io.ReadFull(resp.Body, buf)
		if n > 0 {
			stall.Reset(stallTimeout)
			d.mu.Lock()
			// The chunk's end may have moved down since the request was sent;
			// never write past it, so nothing is fetched twice into the file.
			allowed := min(int64(n), seg.end-seg.pos)
			off := seg.pos
			seg.pos += allowed
			d.mu.Unlock()
			if allowed > 0 {
				if _, err := d.file.WriteAt(buf[:allowed], off); err != nil {
					return &fetchError{kind: kindDisk, msg: err.Error()}
				}
				d.mu.Lock()
				seg.written = off + allowed
				d.mu.Unlock()
				w.bytes.Add(allowed)
				ir.bytes.Add(allowed)
			}
			if allowed < int64(n) {
				return nil
			}
		}
		d.mu.Lock()
		finished := seg.pos >= seg.end
		d.mu.Unlock()
		if finished {
			return nil
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if rctx.Err() != nil {
				return &fetchError{kind: kindNet, msg: "no data for 30s"}
			}
			if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
				return &fetchError{kind: kindNet, msg: "connection closed early"}
			}
			return rerr
		}
	}
}

func (d *Download) spawnStreamLocked(ctx context.Context, ifc Iface, l Link) {
	wctx, cancel := context.WithCancel(ctx)
	w := &worker{d: d, iface: ifc.ID, client: ifc.Client, link: l, ir: d.ifRun(ifc.ID), cancel: cancel}
	d.workers[w] = struct{}{}
	d.ifRun(ifc.ID).active++
	d.wg.Add(1)
	go w.stream(wctx)
}

// stream downloads in one piece, for servers without range support.
func (w *worker) stream(ctx context.Context) {
	d := w.d
	defer d.wg.Done()
	defer w.cancel()
	err := w.streamOnce(ctx)
	d.mu.Lock()
	if err == nil {
		d.streamDone = true
	} else if ctx.Err() == nil {
		d.streamPos = 0
		d.workerFailedLocked(w, err)
		d.ifRun(w.iface).pausedUntil = time.Now().Add(10 * time.Second)
	}
	delete(d.workers, w)
	d.ifRun(w.iface).active--
	d.mu.Unlock()
}

func (w *worker) streamOnce(ctx context.Context) error {
	d := w.d
	if err := d.file.Truncate(0); err != nil {
		return &fetchError{kind: kindDisk, msg: err.Error()}
	}
	d.mu.Lock()
	d.streamPos = 0
	d.mu.Unlock()

	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stall := time.AfterFunc(stallTimeout, cancel)
	defer stall.Stop()
	req, err := newRequest(rctx, w.link, d.st.UserAgent)
	if err != nil {
		return err
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if fe := statusError(resp); fe != nil {
		return fe
	}
	ir := w.ir
	buf := make([]byte, bufSize)
	var pos int64
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			stall.Reset(stallTimeout)
			if _, err := d.file.WriteAt(buf[:n], pos); err != nil {
				return &fetchError{kind: kindDisk, msg: err.Error()}
			}
			pos += int64(n)
			w.bytes.Add(int64(n))
			ir.bytes.Add(int64(n))
			d.mu.Lock()
			d.streamPos = pos
			d.mu.Unlock()
		}
		if rerr == io.EOF {
			if d.st.Size >= 0 && pos != d.st.Size {
				return &fetchError{kind: kindNet, msg: "connection closed early"}
			}
			return nil
		}
		if rerr != nil {
			if rctx.Err() != nil && ctx.Err() == nil {
				return &fetchError{kind: kindNet, msg: "no data for 30s"}
			}
			return rerr
		}
	}
}

func (d *Download) statsLocked(now time.Time) {
	dt := now.Sub(d.lastTick)
	if dt <= 0 {
		return
	}
	d.lastTick = now
	for w := range d.workers {
		w.speed = w.ewma.Update(w.bytes.Load(), dt)
	}
	for _, ir := range d.ifs {
		ir.ewma.Update(ir.bytes.Load(), dt)
	}
	// Prune finished chunks so the list stays short.
	kept := d.segs[:0]
	for _, s := range d.segs {
		if s.owner != nil || s.written < s.end {
			kept = append(kept, s)
		}
	}
	d.segs = kept

	done := d.doneLocked()
	d.meter.Add(now, done)
	d.speed = d.total.Update(done, dt)
	// The estimate uses the long window and only moves every 5 seconds.
	if now.Sub(d.etaAt) >= 5*time.Second {
		d.etaAt = now
		d.eta = -1
		if r := d.meter.Rate(); r > 0 && d.st.Size > 0 {
			d.eta = float64(d.st.Size-done) / r
		}
	}
}

func (d *Download) complete(ctx context.Context) error {
	d.mu.Lock()
	f := d.file
	d.file = nil
	sparse := d.st.RangeOK
	sha, md := d.st.SHA256, d.st.MD5
	part := d.partPath()
	dir, name := d.st.Dir, d.st.Filename
	d.mu.Unlock()

	if f != nil {
		if sparse {
			winutil.SetSparse(f, false)
		}
		f.Sync()
		f.Close()
	}
	if sha != "" || md != "" {
		d.mu.Lock()
		d.st.Status = StatusVerifying
		d.mu.Unlock()
		d.env.Changed(d)
		if err := verify(ctx, part, sha, md); err != nil {
			return err
		}
	}
	final := filepath.Join(dir, uniqueFinal(dir, name))
	if err := os.Rename(part, final); err != nil {
		return fmt.Errorf("could not rename finished file: %w", err)
	}
	d.mu.Lock()
	d.st.Filename = filepath.Base(final)
	d.mu.Unlock()
	return nil
}

func verify(ctx context.Context, path, wantSHA, wantMD5 string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var hs []io.Writer
	var sh, mh hash.Hash
	if wantSHA != "" {
		sh = sha256.New()
		hs = append(hs, sh)
	}
	if wantMD5 != "" {
		mh = md5.New()
		hs = append(hs, mh)
	}
	if _, err := io.Copy(io.MultiWriter(hs...), ctxReader{ctx, f}); err != nil {
		return err
	}
	if sh != nil && hex.EncodeToString(sh.Sum(nil)) != wantSHA {
		return errors.New("SHA-256 does not match")
	}
	if mh != nil && hex.EncodeToString(mh.Sum(nil)) != wantMD5 {
		return errors.New("MD5 does not match")
	}
	return nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func numbered(name string, i int) string {
	ext := filepath.Ext(name)
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), i, ext)
}

// uniqueName picks a name whose final file and part file are both unused.
func uniqueName(dir, name string) string {
	cand := name
	for i := 1; exists(filepath.Join(dir, cand)) || exists(filepath.Join(dir, cand+partSuffix)); i++ {
		cand = numbered(name, i)
	}
	return cand
}

func uniqueFinal(dir, name string) string {
	cand := name
	for i := 1; exists(filepath.Join(dir, cand)); i++ {
		cand = numbered(name, i)
	}
	return cand
}

// shortErr keeps error messages short and free of the link itself, which is private.
func shortErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && len(s) > 120 {
		s = s[i+2:]
	}
	return s
}

func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

type LinkSummary struct {
	Status    LinkStatus `json:"status"`
	Error     string     `json:"error,omitempty"`
	Speed     float64    `json:"speed"`
	Conns     int        `json:"conns"`
	Resolved  bool       `json:"resolved"` // fetched by the browser for this interface
	NeedsPage bool       `json:"needsPage,omitempty"`
}

// Summary is what the UI shows for a download.
type Summary struct {
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
	ETA      float64                `json:"eta"` // seconds, -1 when unknown
	RangeOK  bool                   `json:"rangeOk"`
	Probed   bool                   `json:"probed"`
	SHA256   string                 `json:"sha256,omitempty"`
	MD5      string                 `json:"md5,omitempty"`
	Links    map[string]LinkSummary `json:"links"`
	Added    time.Time              `json:"added"`
	Finished time.Time              `json:"finished,omitzero"`
}

func (d *Download) Summary() Summary {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := Summary{
		ID: d.st.ID, URL: d.st.URL, PageURL: d.st.PageURL, Filename: d.st.Filename, Dir: d.st.Dir,
		Size: d.st.Size, Done: d.doneLocked(), Status: d.st.Status, Error: d.st.Error,
		Speed: d.speed, ETA: -1, RangeOK: d.st.RangeOK, Probed: d.st.Probed,
		SHA256: d.st.SHA256, MD5: d.st.MD5, Links: map[string]LinkSummary{},
		Added: d.st.Added, Finished: d.st.Finished,
	}
	if d.running {
		s.ETA = d.eta
	}
	for id, l := range d.st.Links {
		ls := LinkSummary{Status: l.Status, Error: l.Error, Resolved: l.URL != d.st.URL, NeedsPage: l.NeedsPage}
		if ir := d.ifs[id]; ir != nil && d.running {
			ls.Speed, ls.Conns = ir.ewma.Value(), ir.active
		}
		s.Links[id] = ls
	}
	return s
}
