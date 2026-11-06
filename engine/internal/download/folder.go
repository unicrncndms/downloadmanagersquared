package download

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"downloadmanagersquared/engine/internal/archive"
)

// Folder is a download's Download as folder state: a ZIP whose Entries are
// extracted while it downloads, each Entry's bytes freed from the .part once
// it is extracted and checked (ADR 0006).
type Folder struct {
	On   bool `json:"on"`
	Auto bool `json:"auto,omitempty"` // from the default: on once the name is known to end in .zip

	Into string `json:"into,omitempty"` // the folder to extract into, "" until confirmed
	Out  string `json:"out,omitempty"`  // the output folder in it, once named

	Done   []int64 `json:"done,omitempty"`   // the Start of every Entry done
	Freed  []Range `json:"freed,omitempty"`  // bytes of the .part freed; any lock the mode
	Failed []int64 `json:"failed,omitempty"` // Entries whose bytes came out wrong once, by Start

	Files   int      `json:"files,omitempty"` // files extracted
	Of      int      `json:"of,omitempty"`    // files in the archive
	Renamed []string `json:"renamed,omitempty"`
	Note    string   `json:"note,omitempty"` // why it fell back to a normal download, or what wasn't checked

	Moving    string `json:"moving,omitempty"`    // the folder it is moving to, until it has
	MoveError string `json:"moveError,omitempty"` // why the last move didn't happen
	MoveOut   string `json:"moveOut,omitempty"`   // where a move between drives copies the folder to, for a retry after a crash
	MovePart  string `json:"movePart,omitempty"`  // and the .part
}

func (f *Folder) locked() bool { return f != nil && len(f.Freed) > 0 }

func (f *Folder) clone() *Folder {
	if f == nil {
		return nil
	}
	c := *f
	c.Done, c.Freed, c.Failed = slices.Clone(f.Done), slices.Clone(f.Freed), slices.Clone(f.Failed)
	c.Renamed = slices.Clone(f.Renamed)
	return &c
}

// FolderSummary is what the UI shows of Download as folder.
type FolderSummary struct {
	On      bool     `json:"on"`
	Into    string   `json:"into,omitempty"`
	Out     string   `json:"out,omitempty"`
	Locked  bool     `json:"locked,omitempty"` // can't be switched off: Entries are extracted and freed
	Files   int      `json:"files"`
	Of      int      `json:"of"`
	Renamed []string `json:"renamed,omitempty"`
	Note    string   `json:"note,omitempty"`
	Why     string   `json:"why,omitempty"` // why the switch can't change: locked, not a .zip, a checksum typed, no ranges

	Moving      string `json:"moving,omitempty"`      // the folder it is moving to, until it has
	MovingNow   bool   `json:"movingNow,omitempty"`   // copying to another drive right now
	MoveError   string `json:"moveError,omitempty"`   // why the last move didn't happen
	Refetch     int64  `json:"refetch,omitempty"`     // bytes a move now would download again
	MoveWarning string `json:"moveWarning,omitempty"` // what Details says of that before a move
}

func (f *Folder) summary() *FolderSummary {
	if f == nil {
		return nil
	}
	return &FolderSummary{On: f.On, Into: f.Into, Out: f.Out, Locked: f.locked(), Files: f.Files, Of: f.Of,
		Renamed: slices.Clone(f.Renamed), Note: f.Note, Moving: f.Moving, MoveError: f.MoveError}
}

// folderSummaryLocked is the toggle Details shows: for a download whose name
// ends in .zip, or once the mode was asked for, with why it can't change.
func (d *Download) folderSummaryLocked() *FolderSummary {
	s := d.st.Folder.summary()
	if s == nil {
		if !isZip(d.st.Filename) {
			return nil
		}
		s = &FolderSummary{}
	}
	s.Why = d.folderWhyNotLocked()
	s.MovingNow = d.moving
	if s.On && s.Out != "" {
		s.Refetch = d.moveRefetchLocked()
	}
	return s
}

// folderWhyNotLocked is why the switch can't change, as SetFolder refuses it:
// locked, or (to switch it on) not a .zip, a checksum typed, no ranges. ""
// when it can; a complete download has no switch.
func (d *Download) folderWhyNotLocked() string {
	switch {
	case d.st.Folder.locked():
		return lockedNote
	case d.st.Filename != "" && !isZip(d.st.Filename):
		return notZipNote
	case d.st.SHA256 != "" || d.st.MD5 != "":
		return checksumNote
	case d.st.Probed && !d.st.RangeOK:
		return noRangesNote
	}
	return ""
}

func isZip(name string) bool { return strings.EqualFold(filepath.Ext(name), ".zip") }

// folderOnLocked: the download is a Download as folder.
func (d *Download) folderOnLocked() bool { return d.st.Folder != nil && d.st.Folder.On }

const (
	lockedNote   = "Some files are already extracted and their space freed, so this can't change any more"
	notZipNote   = "Only a .zip can be downloaded as a folder"
	checksumNote = "A checksum is entered, and only a whole file can be checked against it: clear it first"
	noRangesNote = "The server doesn't allow split downloading, so it can't be extracted as it downloads"
	hashNote     = "Downloaded as a file: a checksum was entered, and only a whole file can be checked against it"
	serverMD5    = "The server's MD5 wasn't checked: the archive was extracted as it downloaded, so there was no whole file to check"
)

// SetFolder switches Download as folder on or off. It can't change once
// anything has been freed, and can't go on for a file that isn't a .zip, with
// a checksum typed in, or when the server doesn't allow ranges.
func (d *Download) SetFolder(on bool) error {
	// Not while Entries are being freed: once one is, the mode is locked.
	d.xmu.Lock()
	d.mu.Lock()
	err := d.setFolderLocked(on)
	restart := err == nil && d.running && d.moveNeededLocked()
	d.mu.Unlock()
	d.xmu.Unlock()
	if err != nil {
		return err
	}
	if restart {
		d.restart()
	}
	d.env.Changed(d)
	return nil
}

func (d *Download) setFolderLocked(on bool) error {
	f := d.st.Folder
	switch {
	case d.st.Status == StatusCompleted:
		return errors.New("the download is complete")
	case f.locked():
		//lint:ignore ST1005 shown to the user as is, like the pause messages
		return errors.New(lockedNote)
	case on && d.folderWhyNotLocked() != "":
		return errors.New(d.folderWhyNotLocked())
	}
	if f == nil {
		f = &Folder{}
		d.st.Folder = f
	}
	f.On, f.Auto, f.Note = on, false, ""
	if on && f.Into != "" && !d.st.Allocated {
		d.st.Dir = f.Into
	}
	if !on {
		d.folderOffLocked()
	}
	return nil
}

// folderOffLocked tidies up after the mode goes off before anything was
// freed: the output folder goes if nothing is in it, a move not yet done is
// called off, and the tail is no longer fetched first.
func (d *Download) folderOffLocked() {
	f := d.st.Folder
	if f.Out != "" && os.Remove(f.Out) == nil {
		f.Out = ""
	}
	f.Files, f.Of, f.Renamed = 0, 0, nil
	f.Moving, f.MoveError = "", ""
	if d.plan != nil {
		d.plan.prefer, d.plan.bounds = -1, nil
	}
}

// SetFolderInto sets the folder to extract into (the output folder goes
// inside it). The .part goes next to the output folder. Once extraction has
// started, it moves what is extracted and the .part there (see move.go).
func (d *Download) SetFolderInto(dir string) error {
	if !filepath.IsAbs(dir) {
		return errors.New("not a folder: " + dir)
	}
	dir = filepath.Clean(dir)
	d.mu.Lock()
	if d.st.Status == StatusCompleted {
		d.mu.Unlock()
		return errors.New("the download is complete")
	}
	f := d.st.Folder
	if f == nil {
		f = &Folder{}
		d.st.Folder = f
	}
	if f.On && (f.Out != "" || f.Moving != "") {
		return d.moveToLocked(dir) // unlocks
	}
	f.Into = dir
	if f.On && !d.st.Allocated {
		d.st.Dir = dir
	}
	restart := d.running && d.moveNeededLocked()
	d.mu.Unlock()
	if restart {
		d.restart()
	}
	d.env.Changed(d)
	return nil
}

// moveNeededLocked: the .part exists somewhere other than the folder it
// extracts into, so the next run moves it there first (relocate).
func (d *Download) moveNeededLocked() bool {
	f := d.st.Folder
	return d.folderOnLocked() && f.Into != "" && d.st.Allocated && !strings.EqualFold(d.st.Dir, f.Into)
}

// stopRestart is Download.stopAs for a run that stops only to start again.
const stopRestart Status = "restart"

// restart stops a running download and starts it again, so the file can be
// moved while nothing has it open. It stays running throughout, so its Slot
// isn't given up.
func (d *Download) restart() {
	d.mu.Lock()
	if !d.running {
		d.mu.Unlock()
		return
	}
	d.stopAs = stopRestart
	cancel, done := d.cancel, d.done
	d.mu.Unlock()
	cancel()
	<-done
}

// folderProbedLocked settles the mode once the Probe has said what the file is.
func (d *Download) folderProbedLocked() {
	f := d.st.Folder
	if f == nil {
		return
	}
	if f.Auto {
		f.Auto = false
		f.On = isZip(d.st.Filename) && d.st.SHA256 == "" && d.st.MD5 == ""
	}
	switch {
	case !f.On:
		return
	case !isZip(d.st.Filename):
		d.st.Folder = nil // not offered
		return
	case !d.st.RangeOK:
		f.On, f.Note = false, noRangesNote
		return
	}
	if f.Into != "" && !d.st.Allocated {
		d.st.Dir = f.Into
	}
}

// folderSettledLocked: nothing is left to extract, so the download may finish.
func (d *Download) folderSettledLocked() bool {
	return !d.folderOnLocked() || d.fold != nil && d.fold.finished
}

// folderRun extracts while the download runs, on its own goroutine.
type folderRun struct {
	need     int64 // bytes of the archive's tail the central directory needs
	arc      *archive.Archive
	x        *archive.Extractor
	finished bool // every Entry done (guarded by d.mu)
	again    bool // a large Entry has more bytes on disk to extract now (folderLoop only)

	cancel context.CancelFunc
	done   chan struct{}
}

// afterExtract is called after each extraction that finished Entries, with
// the .part and how many of its bytes have been downloaded; tests set it.
var afterExtract func(f *os.File, onDisk int64)

// startFolderLocked starts extracting, with the file open and the mode on.
func (d *Download) startFolderLocked(ctx context.Context) {
	if d.fold != nil || !d.folderOnLocked() || d.file == nil || !d.plan.splittable {
		return
	}
	fctx, cancel := context.WithCancel(ctx)
	fr := &folderRun{need: min(archive.TailGuess, d.st.Size), cancel: cancel, done: make(chan struct{})}
	d.fold = fr
	d.wantTailLocked(fr)
	go d.folderLoop(fctx, fr)
}

// stopFolder stops extracting and waits until it has, so the file can close.
func (d *Download) stopFolder() {
	d.mu.Lock()
	fr := d.fold
	d.mu.Unlock()
	if fr != nil {
		fr.cancel()
		<-fr.done
	}
}

// wantTailLocked has the tail fetched before anything else when it isn't on
// disk yet, and reports whether it is.
func (d *Download) wantTailLocked(fr *folderRun) bool {
	start := d.st.Size - fr.need
	if onDisk(d.plan.ranges(), start, d.st.Size) {
		d.plan.prefer = -1
		return true
	}
	d.plan.cut(start)
	d.plan.prefer = start
	return false
}

// onDisk: none of [start, end) is missing.
func onDisk(missing []Range, start, end int64) bool {
	for _, r := range missing {
		if r.Start < end && start < r.End {
			return false
		}
	}
	return true
}

func (d *Download) folderLoop(ctx context.Context, fr *folderRun) {
	defer func() {
		if fr.x != nil {
			// A large Entry under way is dropped: the next run puts its
			// freed bytes back in the plan and extracts it from its front.
			fr.x.Close()
		}
		d.mu.Lock()
		if d.fold == fr {
			d.fold = nil
		}
		d.mu.Unlock()
		close(fr.done)
	}()
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		more, err := d.folderStep(ctx, fr)
		if err != nil && ctx.Err() == nil {
			select {
			case d.fatal <- err:
			default:
			}
		}
		if err != nil || !more {
			return
		}
		if fr.again {
			continue // freed bytes are saved: on with the large Entry
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// folderStep does what can be done now: read the central directory once its
// bytes are there, then extract the Entries whose bytes are. more is false
// once the mode is off.
func (d *Download) folderStep(ctx context.Context, fr *folderRun) (more bool, err error) {
	d.mu.Lock()
	if !d.folderOnLocked() {
		d.mu.Unlock()
		return false, nil
	}
	f := d.st.Folder
	file, size, name := d.file, d.st.Size, d.st.Filename
	into, out := f.Into, f.Out
	tail := fr.arc != nil || d.wantTailLocked(fr)
	d.mu.Unlock()

	if fr.arc == nil {
		if !tail {
			return true, nil
		}
		n, err := archive.Need(file, size, fr.need)
		if err == nil && n > fr.need {
			fr.need = n
			d.mu.Lock()
			d.wantTailLocked(fr)
			d.mu.Unlock()
			return true, nil
		}
		var a *archive.Archive
		if err == nil {
			a, err = archive.Read(file, size)
		}
		if err != nil {
			return d.folderFallback(err)
		}
		d.mu.Lock()
		fr.arc = a // under the lock: Details reads it for the move warning
		if d.folderOnLocked() {
			d.plan.bounds = entryBounds(a)
		}
		d.reclaimLocked(a)
		f.Renamed = nil
		for _, r := range a.Renamed {
			f.Renamed = append(f.Renamed, r.From+" → "+r.To)
		}
		d.mu.Unlock()
		d.env.Changed(d)
	}
	if into == "" {
		return true, nil // extraction waits for the folder
	}

	if fr.x == nil {
		if out == "" {
			if out, err = d.nameOut(fr.arc, into, name); err != nil {
				return false, err
			}
		} else if !exists(out) {
			return false, errFolderRemoved
		}
		d.mu.Lock()
		done := slices.Clone(f.Done)
		d.mu.Unlock()
		if err := checkSpace(into, spaceNeeded(fr.arc, done)); err != nil {
			return false, err
		}
		x, err := archive.NewExtractor(fr.arc, file, out, done)
		if err != nil {
			return d.folderFallback(err)
		}
		x.Mark = d.markFunc()
		fr.x = x
		d.mu.Lock()
		f.Files, f.Of = x.Progress()
		d.mu.Unlock()
		d.env.Changed(d)
	}
	if fr.finished {
		return true, nil
	}
	if !exists(out) {
		return false, errFolderRemoved
	}

	d.xmu.Lock()
	defer d.xmu.Unlock()
	d.mu.Lock()
	on := d.folderOnLocked() // it may have gone off since
	missing := d.plan.ranges()
	d.mu.Unlock()
	if !on {
		return false, nil
	}
	am := make([]archive.Range, len(missing))
	for i, r := range missing {
		am[i] = archive.Range{Start: r.Start, End: r.End}
	}
	res, xerr := fr.x.Extract(ctx, am)
	fr.again = res.More
	perr := d.folderRecord(fr, res)
	if xerr != nil && !exists(out) {
		xerr = errFolderRemoved
	}
	return true, cmp.Or(perr, xerr)
}

var errFolderRemoved = &pauseError{msg: "Paused: the folder was removed"}

// folderFallback turns the mode off for an archive it can't handle: nothing
// has been freed, so the download goes on as a normal one.
func (d *Download) folderFallback(err error) (bool, error) {
	var u *archive.Unsupported
	if !errors.As(err, &u) {
		return false, fmt.Errorf("reading the archive's list of files: %w", err)
	}
	d.mu.Lock()
	d.folderOffLocked()
	d.st.Folder.On, d.st.Folder.Note = false, u.Reason+", so it is downloaded as a .zip"
	d.mu.Unlock()
	log.Printf("download %s: not as a folder: %s", d.st.ID, u.Reason)
	d.env.Changed(d)
	return false, nil
}

// nameOut picks and makes the output folder: the archive's Root, or else the
// ZIP's name without .zip, numbered if taken.
func (d *Download) nameOut(a *archive.Archive, into, zipName string) (string, error) {
	base := a.Root
	if base == "" {
		base = zipName[:len(zipName)-len(filepath.Ext(zipName))]
	}
	if strings.TrimSpace(base) == "" {
		base = "download" // a ZIP named only ".zip"
	}
	if err := os.MkdirAll(into, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(into, uniqueName(into, base, ""))
	if err := os.Mkdir(out, 0o755); err != nil {
		return "", err
	}
	d.mu.Lock()
	d.st.Folder.Out = out
	d.mu.Unlock()
	d.env.Changed(d)
	return out, nil
}

// spaceNeeded is the room extraction still needs: every Entry's size plus the
// largest one's compressed size as a margin, less what is already extracted.
func spaceNeeded(a *archive.Archive, done []int64) int64 {
	was := map[int64]bool{}
	for _, s := range done {
		was[s] = true
	}
	n := a.Total + a.LargestCompressed
	for _, e := range a.Entries {
		if was[e.Start] {
			n -= e.Size
		}
	}
	return n
}

// folderRecord saves what an extraction did. An Entry whose bytes came out
// wrong is fetched again; the second time, the download pauses.
func (d *Download) folderRecord(fr *folderRun, res archive.Result) error {
	d.mu.Lock()
	f := d.st.Folder
	for _, e := range res.Done {
		f.Done = append(f.Done, e.Start)
	}
	for _, r := range res.Freed {
		f.Freed = append(f.Freed, Range{r.Start, r.End})
	}
	f.Freed = mergeRanges(f.Freed)
	f.Files, f.Of = fr.x.Progress()
	var err error
	for _, fl := range res.Failed {
		d.refetchLocked(Range{fl.Range.Start, fl.Range.End})
		if i := slices.Index(f.Failed, fl.Range.Start); i >= 0 {
			// Its next try after a resume gets another fetch first.
			f.Failed = slices.Delete(f.Failed, i, i+1)
			err = &pauseError{msg: fmt.Sprintf("Paused: %s came out wrong twice (%s). Resume to download it again", fl.Entry.Name, fl.Reason)}
		} else {
			f.Failed = append(f.Failed, fl.Range.Start)
			log.Printf("download %s: %s, fetching it again", d.st.ID, fl)
		}
	}
	fr.finished = fr.x.Finished()
	onDisk := d.st.Size - d.plan.remaining()
	file := d.file
	changed := len(res.Done) > 0 || len(res.Freed) > 0 || len(res.Failed) > 0
	d.mu.Unlock()
	if changed {
		if afterExtract != nil && len(res.Freed) > 0 {
			afterExtract(file, onDisk)
		}
		d.env.Changed(d)
	}
	return err
}

// mergeRanges sorts ranges and joins those that touch.
func mergeRanges(rs []Range) []Range {
	slices.SortFunc(rs, func(a, b Range) int { return cmp.Compare(a.Start, b.Start) })
	out := rs[:0]
	for _, r := range rs {
		if n := len(out); n > 0 && r.Start <= out[n-1].End {
			out[n-1].End = max(out[n-1].End, r.End)
			continue
		}
		out = append(out, r)
	}
	return out
}

// thousands writes n with commas: 1,280.
func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// refetchLocked has the bytes r fetched again: those on disk go back in the
// plan, and those freed are no longer counted as freed.
func (d *Download) refetchLocked(r Range) {
	for _, p := range subtractRanges([]Range{r}, d.plan.ranges()) {
		d.plan.add(p)
	}
	f := d.st.Folder
	f.Freed = subtractRanges(f.Freed, []Range{r})
}

// reclaimLocked puts back in the plan the bytes freed of Entries not done: a
// large Entry stopped partway (a pause, a crash, a failed run) is extracted
// again from its front, so the bytes freed behind its decompressor are
// fetched again first.
func (d *Download) reclaimLocked(a *archive.Archive) {
	f := d.st.Folder
	if len(f.Freed) == 0 {
		return
	}
	done := map[int64]bool{}
	for _, s := range f.Done {
		done[s] = true
	}
	var lost []Range
	for _, e := range a.Entries {
		if done[e.Start] {
			continue
		}
		n := len(lost)
		for _, r := range f.Freed {
			if r.Start < e.End && e.Start < r.End {
				lost = append(lost, Range{max(r.Start, e.Start), min(r.End, e.End)})
			}
		}
		if len(lost) > n {
			log.Printf("download %s: %s was stopped partway, fetching its freed bytes again", d.st.ID, e.Name)
		}
	}
	for _, r := range lost {
		d.refetchLocked(r)
	}
}

// subtractRanges is rs without the bytes in cut. rs is left alone.
func subtractRanges(rs, cut []Range) []Range {
	out := slices.Clone(rs)
	for _, c := range cut {
		var next []Range
		for _, r := range out {
			if c.End <= r.Start || r.End <= c.Start {
				next = append(next, r)
				continue
			}
			if r.Start < c.Start {
				next = append(next, Range{r.Start, c.Start})
			}
			if c.End < r.End {
				next = append(next, Range{c.End, r.End})
			}
		}
		out = next
	}
	return out
}
