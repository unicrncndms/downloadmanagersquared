// Package download fetches one file over several interfaces at once. Each
// interface has its own link to the file and runs several range requests; idle
// connections take over the far end of the slowest connection's chunk.
package download

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/rate"
	"downloadmanagersquared/engine/internal/winutil"
)

const (
	minSplit     = 4 << 20   // never create a chunk smaller than this by splitting
	bufSize      = 256 << 10 // read/write unit per connection
	stallTimeout = 30 * time.Second
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

	// A torrent's statuses beyond an HTTP download's. A torrent done seeding is StatusCompleted.
	StatusMetadata Status = "metadata" // a magnet link learning the torrent's files from peers
	StatusChoosing Status = "choosing" // waiting for the user to pick files and a folder
	StatusChecking Status = "checking" // hashing data already on disk
	StatusSeeding  Status = "seeding"  // complete, uploading until the ratio target
)

type Range struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"` // exclusive
}

// State is everything needed to resume a download after a restart.
type State struct {
	ID          string           `json:"id"`
	URL         string           `json:"url"`
	PageURL     string           `json:"pageUrl,omitempty"`
	Cookie      Secret           `json:"cookie,omitempty"`
	Referer     string           `json:"referer,omitempty"`
	Limit       int64            `json:"limit,omitempty"` // download speed cap, bytes per second; 0 = none
	UserAgent   string           `json:"userAgent,omitempty"`
	Browser     string           `json:"browser,omitempty"`     // exe of the browser that handed it over; its pages open there
	CookieStore string           `json:"cookieStore,omitempty"` // that browser's cookie store (container) it came from; resolving starts with its cookies
	Dir         string           `json:"dir"`
	Filename    string           `json:"filename"`
	RemoteName  string           `json:"remoteName,omitempty"` // name the server reports; must match across links
	Size        int64            `json:"size"`
	RangeOK     bool             `json:"rangeOk"`
	Version     Version          `json:"version,omitzero"` // the file's version on the server; every chunk must come from it
	Probed      bool             `json:"probed"`
	Stuck       bool             `json:"stuck,omitempty"` // paused because no connection could reach the file
	VPN         paths.Mode       `json:"vpn,omitempty"`
	WaitVPN     bool             `json:"waitVpn,omitempty"` // paused because no tunnel is up under VPN only
	Allocated   bool             `json:"allocated"`
	SHA256      string           `json:"sha256,omitempty"`
	MD5         string           `json:"md5,omitempty"`
	HashResult  string           `json:"hashResult,omitempty"` // "match", or why the checksums don't match the finished file
	Status      Status           `json:"status"`
	Error       string           `json:"error,omitempty"`
	Links       map[string]*Link `json:"links"`
	Remaining   []Range          `json:"remaining,omitempty"`
	Added       time.Time        `json:"added"`
	Finished    time.Time        `json:"finished,omitzero"`
	Folder      *Folder          `json:"folder,omitempty"`    // Download as folder; nil when never asked for
	Mirrors     []Mirror         `json:"mirrors,omitempty"`   // the file's sources, URL among them; nil for one source
	MetaSize    int64            `json:"metaSize,omitempty"`  // the size a Metalink gives; every mirror must match it
	Group       string           `json:"group,omitempty"`     // the files of one Metalink share it, and the parts of a split archive
	GroupName   string           `json:"groupName,omitempty"` // the Metalink's name, or the split archive's
	Log         []Event          `json:"log,omitempty"`       // the activity log, oldest first, at most maxEvents

	plainCookies bool // read with a cookie saved readable, before encryption
}

// Iface is an interface the engine currently allows downloads to use.
type Iface struct {
	ID     string
	Client *http.Client // probes with it: on a dual-stack interface it races IPv6 against IPv4
	// Clients that connect over one family each ("IPv4", "IPv6"), so a link's
	// chunks keep to the family its probe got; nil without IPv6.
	ByFamily map[string]*http.Client
	Conns    int
}

// clientFor is the client for link l's requests: its family's, else Client.
func (ifc Iface) clientFor(l Link) *http.Client {
	if c := ifc.ByFamily[l.Family]; c != nil {
		return c
	}
	return ifc.Client
}

// Env is the engine as a download sees it. Paths answers for a VPN mode:
// tunnels count as interfaces of their own.
type Env interface {
	Paths(paths.Mode) []Iface // the main path first
	ETAWindow() time.Duration
	RequestResolve(id, ifaceID, pageURL, browser string)
	Changed(d *Download)
	Finished(d *Download)
}

type ifaceRun struct {
	backoff backoff
	active  int
	bytes   atomic.Int64
	ewma    rate.EWMA
}

type pauseError struct {
	msg   string
	stuck bool // no connection can reach the file
}

func (e *pauseError) Error() string { return e.msg }

type Download struct {
	env Env
	mu  sync.Mutex
	st  State

	running  bool
	checking bool // a finished file is being checked against new checksums
	cancel   context.CancelFunc
	done     chan struct{}
	stopAs   Status
	fatal    chan error
	wg       sync.WaitGroup

	file    *os.File
	plan    *plan      // nil unless running with the file open
	fold    *folderRun // extracting, in Download as folder
	parked  *folderRun // extraction kept with its .part open while stopped, to go on where it was
	set     *Set       // the split archive it is a part of, which extracts it
	moving  bool       // copying the folder and the .part to another drive
	mvmu    sync.Mutex // held while the folder and the .part move; taken before mu
	xmu     sync.Mutex // held while Entries are extracted and freed; taken before mu
	workers map[*worker]struct{}
	ifs     map[string]*ifaceRun
	probing map[string]bool

	meter    rate.Meter
	total    rate.EWMA
	speed    float64
	eta      rate.ETA
	lastTick time.Time
	lastSave time.Time
	lastDisk time.Time

	limit *rate.Shaper // st.Limit, over all its connections

	seen map[string]string // what the last summary showed, for the activity log; nil until the first
}

func New(env Env, st State) *Download {
	if st.Links == nil {
		st.Links = map[string]*Link{}
	}
	for _, l := range st.Links {
		l.restored()
	}
	st.Limit = max(st.Limit, 0)
	d := &Download{env: env, st: st, limit: rate.NewShaper()}
	d.limit.SetLimits(0, st.Limit)
	return d
}

// SetLimit caps the download's speed, in bytes per second; 0 is none.
func (d *Download) SetLimit(bps int64) {
	bps = max(bps, 0)
	d.mu.Lock()
	d.st.Limit = bps
	d.mu.Unlock()
	d.limit.SetLimits(0, bps)
	d.env.Changed(d)
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
// Browser is the exe of the browser that handed the download over ("" if none).
// CookieStore is the browser's cookie store the download came from ("" = unknown).
func (d *Download) CookieStore() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.st.CookieStore
}

func (d *Download) Browser() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.st.Browser
}

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
	st.Folder = d.st.Folder.clone()
	st.Mirrors = slices.Clone(d.st.Mirrors)
	st.Log = slices.Clone(d.st.Log)
	return st
}

func (d *Download) syncRemainingLocked() {
	if d.plan != nil && d.plan.splittable {
		d.st.Remaining = d.plan.ranges()
	}
}

// Start begins (or resumes) downloading.
func (d *Download) Start() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running || d.st.Status == StatusCompleted {
		return
	}
	d.startLocked()
}

func (d *Download) startLocked() {
	ctx, cancel := context.WithCancel(context.Background())
	d.running, d.cancel, d.done, d.stopAs = true, cancel, make(chan struct{}), ""
	d.fatal = make(chan error, 1)
	d.st.Status, d.st.Error = StatusRunning, ""
	d.workers, d.ifs, d.probing = map[*worker]struct{}{}, map[string]*ifaceRun{}, map[string]bool{}
	d.meter = rate.Meter{Window: d.env.ETAWindow()}
	d.total, d.speed, d.eta = rate.EWMA{}, 0, rate.ETA{}
	d.lastTick = time.Now()
	for _, l := range d.st.Links {
		l.resume(d.st.Stuck)
	}
	d.st.Stuck, d.st.WaitVPN = false, false
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
	l.resolved(url, cookie, referer)
	d.mu.Unlock()
	d.env.Changed(d)
}

func (d *Download) ResolveFailed(ifaceID, msg string) {
	d.mu.Lock()
	if l := d.st.Links[ifaceID]; l != nil {
		l.resolveFailed(msg)
	}
	d.mu.Unlock()
	d.env.Changed(d)
}

// RetryLink gives a failed or mismatched interface another chance.
func (d *Download) RetryLink(ifaceID string) {
	d.mu.Lock()
	if l := d.st.Links[ifaceID]; l != nil {
		l.retry()
	}
	d.mu.Unlock()
	d.env.Changed(d)
}

// SetPageURL sets the landing page and retries interfaces that were waiting for one.
// A fresh link to the file works as a landing page too: the browser fetches
// it like one. A download paused because nothing could reach the file goes
// back in the queue, so the new page gets used without another click.
func (d *Download) SetPageURL(u string) {
	d.mu.Lock()
	d.st.PageURL = strings.TrimSpace(u)
	wake := false
	if d.st.PageURL != "" {
		for _, l := range d.st.Links {
			l.pageKnown()
		}
		wake = d.st.Stuck && d.st.Status == StatusPaused
	}
	d.mu.Unlock()
	if wake {
		d.Resume()
	}
	d.env.Changed(d)
}

func (d *Download) partPath() string { return filepath.Join(d.st.Dir, d.st.Filename+partSuffix) }

// Path is where the unfinished data lives, or the final file once completed.
// For a Download as folder it is the output folder once it is named.
func (d *Download) Path() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if f := d.st.Folder; f != nil && f.On && f.Out != "" {
		return f.Out
	}
	if d.st.Status == StatusCompleted {
		return filepath.Join(d.st.Dir, d.st.Filename)
	}
	return d.partPath()
}

func (d *Download) run(ctx context.Context) {
	d.relocate(ctx) // a move asked for: before anything is open
	err := d.loop(ctx)
	d.mu.Lock()
	for w := range d.workers {
		w.cancel()
	}
	d.mu.Unlock()
	d.wg.Wait()
	d.mu.Lock()
	// Paused or queued, not failed: a large Entry under way waits as it is,
	// so the bytes freed of it needn't be fetched again.
	keep := err != nil && (d.stopAs != "" || errors.As(err, new(*pauseError)))
	d.mu.Unlock()
	d.stopFolder(keep)

	if err == nil {
		err = d.complete(ctx)
	}

	d.mu.Lock()
	d.syncRemainingLocked()
	if d.file != nil {
		d.file.Sync() // a clean stop: what the saved state records is on disk
		if d.parked != nil {
			d.parked.file = d.file // the parked extraction reads it
		} else {
			d.file.Close()
		}
		d.file = nil
	}
	d.plan = nil
	var pe *pauseError
	finished := false
	if d.stopAs == stopRestart && err == nil {
		d.stopAs = "" // it finished meanwhile
	}
	switch {
	case d.stopAs == stopRestart:
		// Stopped only to start again, keeping its Slot: the queue sees it running throughout.
		old := d.done
		d.startLocked()
		close(old)
		d.mu.Unlock()
		d.env.Changed(d)
		return
	case d.stopAs != "":
		d.st.Status = d.stopAs
	case errors.As(err, &pe):
		d.st.Status, d.st.Error, d.st.Stuck = StatusPaused, pe.msg, pe.stuck
	case err != nil:
		d.st.Status, d.st.Error, finished = StatusFailed, err.Error(), true
	default:
		d.st.Status, d.st.Finished, d.st.Remaining, finished = StatusCompleted, time.Now(), nil, true
		// Nothing left to resume: the site's cookies needn't stay on disk.
		d.st.Cookie = ""
		for _, l := range d.st.Links {
			l.Cookie = ""
		}
	}
	d.speed = 0
	d.eta.Clear()
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
	usable := d.env.Paths(d.VPN())
	mainID := mainOf(usable)
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
		d.ifRun(ifc.ID).backoff.conns = ifc.Conns
		l := d.st.Links[ifc.ID]
		if l == nil {
			l = d.newLinkLocked(ifc.ID, ifc.ID == mainID)
			d.st.Links[ifc.ID] = l
			changed = true
		}
		l.wake(now)
		if l.Status != LinkPending || d.probing[ifc.ID] {
			continue
		}
		if d.mainOnlyLocked(ifc.ID, mainID) {
			continue // don't touch the others
		}
		d.probing[ifc.ID] = true
		l.probing()
		go d.probe(ctx, ifc, *l)
	}
	d.decidePageHitsLocked(byID)
	d.settleHeldLocked(byID, mainID)
	if d.nothingLeftLocked(byID, mainID) {
		if d.allMismatchedLocked(byID, mainID) {
			return false, errChanged // resuming can't help: the file isn't there any more
		}
		msg := "Paused: no connection can reach the file; resume to try them again"
		for _, l := range d.st.Links {
			if l.Status == LinkNeedsPage {
				msg = "Paused: no connection can reach the file; enter the landing page under Details, then resume"
			}
		}
		return false, &pauseError{msg: msg, stuck: true}
	}

	if !d.st.Probed {
		return false, nil
	}
	if d.file == nil {
		if err := d.openFileLocked(); err != nil {
			return false, err
		}
		changed = true
	}
	d.startFolderLocked() // before any claim, so the archive's tail comes first

	for w := range d.workers {
		l := d.st.Links[w.iface]
		_, ok := byID[w.iface]
		if !ok || l == nil || l.Status != LinkReady || l.URL != w.link.URL {
			w.cancel()
		}
	}

	d.trimLocked(byID)

	for _, ifc := range usable {
		l := d.st.Links[ifc.ID]
		if l.Status != LinkReady || !d.plan.splittable && ifc.ID != mainID {
			continue
		}
		ir := d.ifRun(ifc.ID)
		ir.backoff.recover(now)
		if !ir.backoff.mayOpen(now) {
			continue
		}
		for ir.active < ir.backoff.target() {
			seg := d.claimLocked(ifc.ID)
			if seg == nil {
				break
			}
			d.spawnLocked(ctx, ifc, *l, seg)
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
			return false, &pauseError{msg: fmt.Sprintf("Paused: disk almost full (%s free)", human(free))}
		}
	}

	// A Download as folder holds its Slot until the last Entry is checked.
	return d.plan.remaining() == 0 && len(d.workers) == 0 && d.folderSettledLocked(), nil
}

// nothingLeftLocked: every link the download could use has been given up on,
// so it would sit at 0 B/s forever.
func (d *Download) nothingLeftLocked(usable map[string]Iface, mainID string) bool {
	if len(d.workers) > 0 || len(d.probing) > 0 {
		return false
	}
	seen := false
	for id := range usable {
		if d.mainOnlyLocked(id, mainID) {
			continue
		}
		if l := d.st.Links[id]; l == nil || !l.stuck() {
			return false
		}
		seen = true
	}
	return seen
}

// mainOf is the main path of paths as Env.Paths orders them.
func mainOf(paths []Iface) string {
	if len(paths) == 0 {
		return ""
	}
	return paths[0].ID
}

// mainOnlyLocked: without range support only the main interface is used.
func (d *Download) mainOnlyLocked(id, mainID string) bool {
	return d.st.Probed && !d.st.RangeOK && id != mainID
}

func (d *Download) ifRun(id string) *ifaceRun {
	ir := d.ifs[id]
	if ir == nil {
		ir = &ifaceRun{}
		d.ifs[id] = ir
	}
	return ir
}

func (d *Download) openFileLocked() error {
	if p := d.parked; p != nil && p.file != nil {
		d.file, p.file = p.file, nil
		d.plan = newPlan(d.st.Size, true, d.st.Remaining)
		return nil
	}
	if err := os.MkdirAll(d.st.Dir, 0o755); err != nil {
		return err
	}
	if !d.st.Allocated {
		d.st.Filename = uniqueName(d.st.Dir, d.st.Filename, "", partSuffix)
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
		d.file, d.st.Allocated = f, true
		d.plan = newPlan(d.st.Size, false, nil)
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
		if err := checkSpace(d.st.Dir, d.remainingLocked()); err != nil {
			f.Close()
			return err
		}
		d.file = f
	}
	d.plan = newPlan(d.st.Size, true, d.st.Remaining)
	return nil
}

func checkSpace(dir string, need int64) error {
	free, err := winutil.FreeSpace(dir)
	if err != nil {
		return nil
	}
	if free < need+diskReserve {
		return &pauseError{msg: fmt.Sprintf("Paused: not enough disk space (need %s, %s free)", human(need), human(free))}
	}
	return nil
}

func (d *Download) remainingLocked() int64 {
	if d.plan != nil {
		return d.plan.remaining()
	}
	var n int64
	for _, r := range d.st.Remaining {
		n += r.End - r.Start
	}
	return n
}

func (d *Download) doneLocked() int64 {
	switch {
	case d.st.Status == StatusCompleted:
		return d.st.Size
	case !d.st.Allocated:
		return 0
	case d.plan != nil:
		return d.plan.done()
	case !d.st.RangeOK:
		return 0 // the next attempt starts over
	}
	return d.st.Size - d.remainingLocked()
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
	d.plan.prune()

	done := d.doneLocked()
	d.meter.Add(now, done)
	d.speed = d.total.Update(done, dt)
	// The estimate uses the long window.
	if d.st.Size > 0 {
		d.eta.Update(now, d.st.Size-done, d.meter.Rate())
	} else {
		d.eta.Update(now, -1, 0)
	}
}
