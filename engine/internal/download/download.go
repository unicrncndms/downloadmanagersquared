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
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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

// VPNMode is which paths a download may use, fixed when it is added.
type VPNMode string

const (
	VPNOff       VPNMode = ""          // bare interfaces only
	VPNAlongside VPNMode = "alongside" // bare interfaces and their tunnels
	VPNOnly      VPNMode = "vpn"       // tunnels only; pause while none is up
)

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
	Stuck      bool             `json:"stuck,omitempty"` // paused because no connection could reach the file
	VPN        VPNMode          `json:"vpn,omitempty"`
	WaitVPN    bool             `json:"waitVpn,omitempty"` // paused because no tunnel is up under VPN only
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

// Env is the engine as a download sees it. Usable and MainID answer for a VPN
// mode: tunnels count as interfaces of their own.
type Env interface {
	Usable(VPNMode) []Iface
	MainID(VPNMode) string
	ETAWindow() time.Duration
	RequestResolve(id, ifaceID, pageURL string)
	Changed(d *Download)
	Finished(d *Download)
}

type ifaceRun struct {
	backoff backoff
	active  int
	bytes   atomic.Int64
	ewma    rate.EWMA
}

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

type pauseError struct {
	msg   string
	stuck bool // no connection can reach the file
	vpn   bool // no tunnel is up, under VPN only
}

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

	file    *os.File
	plan    *plan // nil unless running with the file open
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
}

func New(env Env, st State) *Download {
	if st.Links == nil {
		st.Links = map[string]*Link{}
	}
	for _, l := range st.Links {
		l.restored()
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

func (d *Download) SetHashes(sha, md string) {
	d.mu.Lock()
	d.st.SHA256, d.st.MD5 = strings.ToLower(strings.TrimSpace(sha)), strings.ToLower(strings.TrimSpace(md))
	d.mu.Unlock()
	d.env.Changed(d)
}

// SetPageURL sets the landing page and retries interfaces that were waiting for one.
func (d *Download) SetPageURL(u string) {
	d.mu.Lock()
	d.st.PageURL = strings.TrimSpace(u)
	if d.st.PageURL != "" {
		for _, l := range d.st.Links {
			l.pageKnown()
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
	d.plan = nil
	var pe *pauseError
	finished := false
	switch {
	case d.stopAs != "":
		d.st.Status = d.stopAs
	case errors.As(err, &pe):
		d.st.Status, d.st.Error, d.st.Stuck, d.st.WaitVPN = StatusPaused, pe.msg, pe.stuck, pe.vpn
	case err != nil:
		d.st.Status, d.st.Error, finished = StatusFailed, err.Error(), true
	default:
		d.st.Status, d.st.Finished, d.st.Remaining, finished = StatusCompleted, time.Now(), nil, true
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
	usable := d.env.Usable(d.st.VPN) // fixed when added: no lock needed
	mainID := d.env.MainID(d.st.VPN)
	if len(usable) == 0 && d.st.VPN == VPNOnly {
		// Never fall back to a bare interface.
		return false, &pauseError{msg: "Paused: no VPN tunnel is up. It resumes by itself once one is.", vpn: true}
	}
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
			l = &Link{URL: d.st.URL, Cookie: d.st.Cookie, Referer: d.st.Referer, Status: LinkPending}
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

	for w := range d.workers {
		l := d.st.Links[w.iface]
		_, ok := byID[w.iface]
		if !ok || l == nil || l.Status != LinkReady || l.URL != w.link.URL {
			w.cancel()
		}
	}

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

	return d.plan.remaining() == 0 && len(d.workers) == 0, nil
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

func (d *Download) probe(ctx context.Context, ifc Iface, l Link) {
	res, err := probeLink(ctx, ifc.Client, l, d.st.UserAgent)
	usable := map[string]Iface{}
	for _, u := range d.env.Usable(d.st.VPN) {
		usable[u.ID] = u
	}
	mainID := d.env.MainID(d.st.VPN)
	d.mu.Lock()
	delete(d.probing, ifc.ID)
	cur := d.st.Links[ifc.ID]
	if cur == nil || cur.URL != l.URL || cur.Status != LinkProbing {
		d.mu.Unlock()
		return
	}
	if ctx.Err() != nil {
		cur.requeue()
		d.mu.Unlock()
		return
	}
	if err == nil && !res.rejected() && res.status < 400 && ifc.ID != mainID && d.mainMayAnswerLocked(usable, mainID) {
		cur.hold(time.Now(), res) // the main interface decides what the file is
	} else {
		d.applyProbeLocked(ifc.ID, cur, res, err)
	}
	d.mu.Unlock()
	d.env.Changed(d)
}

func (d *Download) applyProbeLocked(ifaceID string, l *Link, r probeResult, err error) {
	if err != nil {
		log.Printf("download %s probe via %s: %s", d.st.ID, ifaceID, shortErr(err))
		l.unreachable(time.Now(), shortErr(err))
		return
	}
	log.Printf("download %s probe via %s: HTTP %d, size %d, ranges %v, page %v", d.st.ID, ifaceID, r.status, r.size, r.rangeOK, r.isPage)
	if r.busy() {
		now := time.Now()
		d.ifRun(ifaceID).backoff.pushedBack(now, r.wait)
		l.serverBusy(now, r.wait, fmt.Sprintf("server busy: HTTP %d", r.status))
		return
	}
	if r.rejected() {
		if r.isPage && d.st.PageURL == "" && l.URL == d.st.URL {
			// Either the link is a landing page, or it is the file link and this
			// interface got an error page for it. Decide once every interface has answered.
			l.webPage()
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
		l.httpError(time.Now(), fmt.Sprintf("HTTP %d", r.status))
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
			l.mismatch(fmt.Sprintf("different file size (%d vs %d bytes)", r.size, d.st.Size))
			return
		}
		if d.st.RemoteName != "" && r.name != "" && !strings.EqualFold(r.name, d.st.RemoteName) {
			l.mismatch(fmt.Sprintf("different file name (%q vs %q)", r.name, d.st.RemoteName))
			return
		}
		if d.st.RangeOK && !r.rangeOK {
			l.fail("this link does not allow split downloading")
			return
		}
	}
	l.ready()
}

// decidePageHitsLocked settles interfaces that got a web page for the source URL.
// If another interface got the file, the link is the file and those interfaces
// need their own link from the landing page. Only if no interface got the file is
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
			if l := d.st.Links[id]; l == nil || l.answering() {
				return // still waiting on an interface that might get the file
			}
		}
		d.st.PageURL = d.st.URL
	}
	for _, id := range hits {
		l := d.st.Links[id]
		d.needResolveLocked(id, l, "this connection gets a web page instead of the file")
	}
}

// mainMayAnswerLocked: the main interface's probe is still to come, so answers
// from the other interfaces wait for it.
func (d *Download) mainMayAnswerLocked(usable map[string]Iface, mainID string) bool {
	if d.st.Probed {
		return false
	}
	if _, ok := usable[mainID]; !ok {
		return false
	}
	l := d.st.Links[mainID]
	return l == nil || l.answering()
}

// settleHeldLocked applies held answers once the main interface has decided the
// file, or can't. In the second case the earliest answer decides it.
func (d *Download) settleHeldLocked(usable map[string]Iface, mainID string) {
	if d.mainMayAnswerLocked(usable, mainID) {
		return
	}
	var ids []string
	for id, l := range d.st.Links {
		if l.held != nil {
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, func(a, b string) int { return d.st.Links[a].heldAt.Compare(d.st.Links[b].heldAt) })
	for _, id := range ids {
		l := d.st.Links[id]
		d.applyProbeLocked(id, l, *l.takeHeld(), nil)
	}
}

func (d *Download) needResolveLocked(ifaceID string, l *Link, reason string) {
	if l.needResolve(d.st.PageURL != "", reason) {
		id, page := d.st.ID, d.st.PageURL
		go d.env.RequestResolve(id, ifaceID, page)
	}
}

func (d *Download) openFileLocked() error {
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

// claimLocked gets work from the plan, sized to how fast this interface's
// connections have been.
func (d *Download) claimLocked(ifaceID string) *segment {
	var perConn float64
	if ir := d.ifs[ifaceID]; ir != nil && ir.active > 0 {
		perConn = ir.ewma.Value() / float64(ir.active)
	}
	return d.plan.claim(perConn)
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

func (d *Download) spawnLocked(ctx context.Context, ifc Iface, l Link, seg *segment) {
	wctx, cancel := context.WithCancel(ctx)
	w := &worker{d: d, iface: ifc.ID, client: ifc.Client, link: l, ir: d.ifRun(ifc.ID), cancel: cancel}
	seg.owner = w
	d.workers[w] = struct{}{}
	w.ir.active++
	d.wg.Add(1)
	go w.run(wctx, seg)
}

// nextLocked lets a worker that finished a chunk keep going without waiting for a tick.
// A backoff pause only stops new connections; working ones carry on.
func (d *Download) nextLocked(w *worker) *segment {
	l := d.st.Links[w.iface]
	ir := w.ir
	if l == nil || l.Status != LinkReady || l.URL != w.link.URL || ir.active > ir.backoff.target() {
		return nil
	}
	return d.claimLocked(w.iface)
}

func (d *Download) workerFailedLocked(w *worker, err error) {
	now := time.Now()
	ir := w.ir
	l := d.st.Links[w.iface]
	var fe *fetchError
	if !errors.As(err, &fe) {
		fe = &fetchError{kind: kindNet, msg: shortErr(err)}
	}
	log.Printf("download %s via %s: %s", d.st.ID, w.iface, fe.msg)
	// A rejection while sibling connections on the same link still work is a
	// per-IP connection limit, not an expired link: back off instead.
	if fe.kind == kindExpired && ir.active > 1 {
		fe = &fetchError{kind: kindBusy, msg: fe.msg, retryAfter: 15 * time.Second}
	}
	switch fe.kind {
	case kindExpired:
		if l != nil && l.URL == w.link.URL && l.Status == LinkReady {
			l.expired(fe.msg)
		}
	case kindBusy:
		ir.backoff.pushedBack(now, fe.retryAfter)
	case kindRange:
		if l != nil && l.URL == w.link.URL {
			l.fail(fe.msg)
		}
	case kindDisk:
		select {
		case d.fatal <- errors.New("writing to disk failed: " + fe.msg):
		default:
		}
	default:
		v := ir.backoff.failed(now, fe.kind)
		if l == nil || l.URL != w.link.URL || l.Status != LinkReady {
			break
		}
		switch v {
		case linkUnreachable:
			l.unreachable(now, fe.msg)
		case linkHTTPError:
			l.httpError(now, fe.msg)
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
		d.plan.release(seg)
		switch {
		case ctx.Err() != nil:
			seg = nil
		case err != nil:
			d.workerFailedLocked(w, err)
			seg = nil
		default:
			w.ir.backoff.chunkDone()
			if l := d.st.Links[w.iface]; l != nil && l.URL == w.link.URL {
				l.dataArrived()
			}
			if seg = d.nextLocked(w); seg != nil {
				seg.owner = w
			}
		}
		if seg == nil {
			delete(d.workers, w)
			w.ir.active--
		}
		d.mu.Unlock()
	}
}

func (w *worker) fetch(ctx context.Context, seg *segment, buf []byte) error {
	d := w.d
	d.mu.Lock()
	start, end, ranged := seg.pos, seg.end, d.plan.splittable
	d.mu.Unlock()
	if start >= end {
		return nil
	}
	if !ranged {
		// Without ranges every attempt fetches the whole file again.
		if err := d.file.Truncate(0); err != nil {
			return &fetchError{kind: kindDisk, msg: err.Error()}
		}
	}

	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stall := time.AfterFunc(stallTimeout, cancel)
	defer stall.Stop()

	req, err := newRequest(rctx, w.link, d.st.UserAgent)
	if err != nil {
		return &fetchError{kind: kindRange, msg: err.Error()}
	}
	if ranged {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end-1))
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if fe := statusError(resp); fe != nil {
		return fe
	}
	if ranged {
		if resp.StatusCode != http.StatusPartialContent {
			return &fetchError{kind: kindRange, msg: "server ignored the range request (" + resp.Status + ")"}
		}
		if cs, _, ok := parseContentRange(resp.Header.Get("Content-Range")); !ok || cs != start {
			return &fetchError{kind: kindRange, msg: "server sent the wrong part of the file"}
		}
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
				if end == unknownEnd {
					d.mu.Lock()
					d.plan.endHere(seg)
					d.mu.Unlock()
					return nil
				}
				return &fetchError{kind: kindNet, msg: "connection closed early"}
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
	final := filepath.Join(dir, uniqueName(dir, name, ""))
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

// uniqueName picks a name that is unused with each of the given suffixes.
func uniqueName(dir, name string, suffixes ...string) string {
	taken := func(cand string) bool {
		for _, s := range suffixes {
			if exists(filepath.Join(dir, cand+s)) {
				return true
			}
		}
		return false
	}
	cand := name
	for i := 1; taken(cand); i++ {
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
	Status   LinkStatus `json:"status"`
	Error    string     `json:"error,omitempty"`
	Speed    float64    `json:"speed"`
	Conns    int        `json:"conns"`
	Allowed  int        `json:"allowed,omitempty"`  // connections backoff allows right now
	MaxConns int        `json:"maxConns,omitempty"` // connections the settings allow
	Busy     bool       `json:"busy,omitempty"`     // holding back because the server pushed back
	Resolved bool       `json:"resolved"`           // fetched by the browser for this interface
	Up       float64    `json:"up,omitempty"`       // torrents: upload speed to the peers on this path
}

// Summary is what the UI shows for a download.
type Summary struct {
	Kind     string                 `json:"kind,omitempty"` // "torrent"; empty for HTTP
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

	VPN VPNMode `json:"vpn,omitempty"`

	// Torrents only: the files to choose from (while choosing), then the upload once complete.
	Files    []FileSummary `json:"files,omitempty"`
	AskVPN   bool          `json:"askVpn,omitempty"` // waiting for: VPN, or the current connection?
	Peers    int           `json:"peers,omitempty"`  // connected
	Seeds    int           `json:"seeds,omitempty"`  // connected peers with the whole torrent
	Swarm    int           `json:"swarm,omitempty"`  // peers known, connected or not
	InfoHash string        `json:"infoHash,omitempty"`
	Uploaded int64         `json:"uploaded,omitempty"`
	Ratio    float64       `json:"ratio,omitempty"`
	UpSpeed  float64       `json:"upSpeed,omitempty"`
}

// FileSummary is one file of a torrent.
type FileSummary struct {
	Path     string `json:"path"` // within the torrent, "/"-separated
	Size     int64  `json:"size"`
	Selected bool   `json:"selected"`
}

// WaitingForVPN is whether the download paused only because no tunnel was up.
func (d *Download) WaitingForVPN() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.st.Status == StatusPaused && d.st.WaitVPN
}

func (d *Download) Summary() Summary {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := Summary{
		ID: d.st.ID, URL: d.st.URL, PageURL: d.st.PageURL, Filename: d.st.Filename, Dir: d.st.Dir,
		Size: d.st.Size, Done: d.doneLocked(), Status: d.st.Status, Error: d.st.Error,
		Speed: d.speed, ETA: -1, RangeOK: d.st.RangeOK, Probed: d.st.Probed,
		SHA256: d.st.SHA256, MD5: d.st.MD5, Links: map[string]LinkSummary{},
		Added: d.st.Added, Finished: d.st.Finished, VPN: d.st.VPN,
	}
	if d.running {
		s.ETA = d.eta.Seconds()
	}
	now := time.Now()
	for id, l := range d.st.Links {
		ls := LinkSummary{Status: l.Status, Error: l.Error, Resolved: l.URL != d.st.URL}
		if ir := d.ifs[id]; ir != nil && d.running {
			ls.Speed, ls.Conns = ir.ewma.Value(), ir.active
			ls.Allowed, ls.MaxConns, ls.Busy = ir.backoff.target(), ir.backoff.conns, ir.backoff.busy(now)
		}
		s.Links[id] = ls
	}
	return s
}
