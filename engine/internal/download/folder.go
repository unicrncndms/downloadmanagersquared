package download

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"math"
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
	Auto bool `json:"auto,omitempty"` // from the default: on once the name is known to end in .zip, .7z or .rar

	Into string `json:"into,omitempty"` // the folder to extract into, "" until confirmed
	Out  string `json:"out,omitempty"`  // the output folder in it, once named

	Done   []int64 `json:"done,omitempty"`   // the Start of every Entry done
	Freed  []Range `json:"freed,omitempty"`  // bytes of the .part freed; any lock the mode
	Failed []int64 `json:"failed,omitempty"` // Entries whose bytes came out wrong once, by Start

	Files   int      `json:"files,omitempty"` // files extracted
	Of      int      `json:"of,omitempty"`    // files in the archive
	Renamed []string `json:"renamed,omitempty"`
	Note    string   `json:"note,omitempty"` // why it fell back to a normal download, or what wasn't checked

	// An encrypted RAR or 7z: extraction waits for the password while the
	// download goes on. Ask is AskPassword or AskWrong while it waits.
	Password Secret `json:"password,omitempty"` // until extraction is finished
	Ask      string `json:"ask,omitempty"`

	// Archive keeping (ADR 0007): the choice, "" for the archiveKeeping
	// setting's, and what Automatic decided when extraction started
	// (KeepUntil or KeepFree, "" until then), so a restart keeps to it.
	Keep    string `json:"keep,omitempty"`
	Keeping string `json:"keeping,omitempty"`

	Moving    string `json:"moving,omitempty"`    // the folder it is moving to, until it has
	MoveError string `json:"moveError,omitempty"` // why the last move didn't happen
	MoveOut   string `json:"moveOut,omitempty"`   // where a move between drives copies the folder to, for a retry after a crash
	MovePart  string `json:"movePart,omitempty"`  // and the .part
}

func (f *Folder) locked() bool { return f != nil && len(f.Freed) > 0 }

// The values of archive keeping: Folder.Keep and the archiveKeeping setting.
const (
	KeepAuto  = "auto"           // keep it until extracted when the drives have room, else free it
	KeepFree  = "free"           // free its bytes as they are extracted (ADR 0006)
	KeepUntil = "untilExtracted" // free nothing until every Entry is extracted, then delete it
	KeepAfter = "keep"           // free nothing, and keep it under its own name afterwards
)

// KeepChoice reports whether k is one of the values of archive keeping.
func KeepChoice(k string) bool {
	return k == KeepAuto || k == KeepFree || k == KeepUntil || k == KeepAfter
}

// keepChoice is the archive keeping chosen, def (the setting's) when none
// is. An old state file has none, and so takes the setting's too.
func (f *Folder) keepChoice(def string) string { return cmp.Or(f.Keep, def, KeepAuto) }

// keepNow is the archive keeping in force: KeepFree once anything is freed,
// as those bytes are gone whatever was chosen; for Automatic, what it
// decided, "" until it has.
func (f *Folder) keepNow(def string) string {
	switch k := f.keepChoice(def); {
	case f.locked():
		return KeepFree
	case k == KeepAuto:
		return f.Keeping
	default:
		return k
	}
}

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
	On      bool        `json:"on"`
	Into    string      `json:"into,omitempty"`
	Out     string      `json:"out,omitempty"`
	Locked  bool        `json:"locked,omitempty"` // can't be switched off: Entries are extracted and freed
	Files   int         `json:"files"`
	Of      int         `json:"of"`
	Renamed []string    `json:"renamed,omitempty"`
	Note    string      `json:"note,omitempty"`
	Ask     string      `json:"ask,omitempty"` // AskPassword, AskWrong: the card asks for the archive's password
	Set     *SetSummary `json:"set,omitempty"` // a part of a split archive: what its Set has done
	Why     string      `json:"why,omitempty"` // why the switch can't change: locked, not a .zip, .7z or .rar, a checksum typed, no ranges

	// Archive keeping: the choice (the setting's when none is made), what it
	// does now (for Automatic what it decided, "" until then; KeepFree once
	// anything is freed), and that it can't change any more, as bytes are freed.
	Keep      string `json:"keep,omitempty"`
	Keeping   string `json:"keeping,omitempty"`
	KeepFixed bool   `json:"keepFixed,omitempty"`

	Moving      string `json:"moving,omitempty"`      // the folder it is moving to, until it has
	MovingNow   bool   `json:"movingNow,omitempty"`   // copying to another drive right now
	MoveError   string `json:"moveError,omitempty"`   // why the last move didn't happen
	Refetch     int64  `json:"refetch,omitempty"`     // bytes a move now would download again
	MoveWarning string `json:"moveWarning,omitempty"` // what Details says of that before a move

	// The extraction's progress (a part of a split archive has its Set's
	// instead): bytes of the archive extracted and freed, what it is doing or
	// waiting for, and the seconds left, 0 while unknown.
	Extracted  int64   `json:"extracted,omitempty"`
	Phase      string  `json:"phase,omitempty"`
	ExtractETA float64 `json:"extractEta,omitempty"`
}

func (f *Folder) summary() *FolderSummary {
	if f == nil {
		return nil
	}
	return &FolderSummary{On: f.On, Into: f.Into, Out: f.Out, Locked: f.locked(), Files: f.Files, Of: f.Of,
		Renamed: slices.Clone(f.Renamed), Note: f.Note, Ask: f.Ask, Moving: f.Moving, MoveError: f.MoveError}
}

// What Folder.Ask can be.
const (
	AskPassword = "password" // it's encrypted
	AskWrong    = "wrong"    // the password given didn't open it
)

// SetArchivePassword gives an encrypted archive its password: extraction
// tries it next, and asks again (AskWrong) if it doesn't open the archive.
func (d *Download) SetArchivePassword(password string) error {
	d.mu.Lock()
	f := d.st.Folder
	if !d.folderOnLocked() || f.Ask == "" {
		d.mu.Unlock()
		return errors.New("no password is asked for")
	}
	if password == "" {
		d.mu.Unlock()
		return errors.New("the password is empty")
	}
	f.Password, f.Ask = Secret(password), ""
	d.mu.Unlock()
	d.env.Changed(d)
	return nil
}

// folderSummaryLocked is the toggle Details shows: for a download whose name
// ends in .zip, .7z or .rar, or once the mode was asked for, with why it can't change.
func (d *Download) folderSummaryLocked() *FolderSummary {
	s := d.st.Folder.summary()
	if s == nil {
		if !archive.Extractable(d.st.Filename) {
			return nil
		}
		s = &FolderSummary{}
	}
	s.Why = d.folderWhyNotLocked()
	s.MovingNow = d.moving
	if d.set != nil {
		s.Set = d.set.Summary()
	}
	def := d.keepDefault()
	f := cmp.Or(d.st.Folder, &Folder{})
	s.Keep, s.Keeping, s.KeepFixed = f.keepChoice(def), f.keepNow(def), d.keepFixedLocked()
	if s.On && s.Out != "" {
		s.Refetch = d.moveRefetchLocked()
	}
	if fr := d.fold; fr != nil && s.On && s.Into != "" && d.set == nil && !fr.finished {
		// A kept archive frees nothing: what is extracted is counted instead.
		s.Extracted = max(sumRanges(d.st.Folder.Freed), fr.passed)
		d.pace.add(time.Now(), s.Extracted)
		switch {
		case fr.arc == nil:
			s.Phase = "Waiting for the archive's list of files"
		case fr.idle:
			s.Phase = "Waiting for the download"
		default:
			s.Phase = "Extracting"
		}
		// It can't end before the download does.
		s.ExtractETA = math.Round(max(d.pace.left(d.st.Size), d.eta.Seconds()))
	}
	return s
}

// folderWhyNotLocked is why the switch can't change, as SetFolder refuses it:
// locked, or (to switch it on) not a .zip, .7z or .rar, a checksum typed, no ranges. ""
// when it can; a complete download has no switch.
func (d *Download) folderWhyNotLocked() string {
	switch {
	case d.st.Folder.locked():
		return lockedNote
	case d.st.Filename != "" && !archive.Extractable(d.st.Filename):
		return notZipNote
	case d.st.SHA256 != "" || d.st.MD5 != "":
		return checksumNote
	case d.st.Probed && !d.st.RangeOK:
		return noRangesNote
	}
	return ""
}

// FolderOn: the download is a Download as folder, or will be one by the
// default once it starts (as folderProbedLocked settles it).
func (d *Download) FolderOn() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.st.Folder
	return f != nil && (f.On || f.Auto && archive.Extractable(d.st.Filename) && d.st.SHA256 == "" && d.st.MD5 == "")
}

// folderOnLocked: the download is a Download as folder.
func (d *Download) folderOnLocked() bool { return d.st.Folder != nil && d.st.Folder.On }

const (
	lockedNote   = "Some files are already extracted and their space freed, so this can't change any more"
	notZipNote   = "Only a .zip, .7z or .rar can be downloaded as a folder"
	checksumNote = "A checksum is entered, and only a whole file can be checked against it: clear it first"
	noRangesNote = "The server doesn't allow split downloading, so it can't be extracted as it downloads"
	hashNote     = "Downloaded as a file: a checksum was entered, and only a whole file can be checked against it"
	serverMD5    = "The server's checksum wasn't checked: the archive was extracted as it downloaded, so there was no whole file to check"
)

// SetFolder switches Download as folder on or off. It can't change once
// anything has been freed, and can't go on for a file that isn't a .zip, .7z or .rar, with
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
	case d.st.Status == StatusCompleted && d.inSetLocked():
		return d.partFolderLocked(on)
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

// partFolderLocked switches Download as folder for a part of a split archive
// that is already fetched, with the rest of its Set: its file becomes the
// .part the Set reads and deletes. Off, the Set gives the .part its name
// back (Set.step), as it may have it open. Not once any of it is freed.
func (d *Download) partFolderLocked(on bool) error {
	f := d.st.Folder
	switch {
	case f.locked():
		//lint:ignore ST1005 shown to the user as is, like the pause messages
		return errors.New(lockedNote)
	case on == d.folderOnLocked():
		return nil
	case !on:
		f.On = false
		return nil
	}
	if err := os.Rename(filepath.Join(d.st.Dir, d.st.Filename), d.partPath()); err != nil {
		return err
	}
	if f == nil {
		f = &Folder{}
		d.st.Folder = f
	}
	f.On, f.Auto, f.Note = true, false, ""
	return nil
}

// folderOffLocked tidies up after the mode goes off before anything was
// freed: the output folder goes if nothing is in it, a move not yet done is
// called off, and the tail is no longer fetched first.
func (d *Download) folderOffLocked() {
	f := d.st.Folder
	if p := d.parked; p != nil {
		// Nothing starts it again with the mode off: close it, so its
		// decompressor and its files in the output folder are let go.
		d.parked = nil
		p.ex.close()
		if p.file != nil {
			p.file.Close()
		}
	}
	if f.Out != "" && os.Remove(f.Out) == nil {
		f.Out = ""
	}
	f.Files, f.Of, f.Renamed = 0, 0, nil
	f.Password, f.Ask = "", ""
	f.Moving, f.MoveError = "", ""
	if d.plan != nil {
		d.plan.prefer, d.plan.bounds = -1, nil
	}
}

// keepFixedLocked: bytes of the archive are freed, in this download or (a
// part of a split archive) in another part of its Set, so archive keeping
// can't change any more.
func (d *Download) keepFixedLocked() bool {
	return d.st.Folder.locked() || d.set != nil && d.set.Summary() != nil && d.set.Summary().Locked
}

// SetArchiveKeeping sets what a Download as folder does with its archive's
// bytes (KeepAuto, KeepFree, KeepUntil or KeepAfter), until anything is freed.
func (d *Download) SetArchiveKeeping(k string) error {
	if !KeepChoice(k) {
		return fmt.Errorf("not a way to keep the archive: %q", k)
	}
	// Not while Entries are being freed: once one is, it can't change.
	d.xmu.Lock()
	d.mu.Lock()
	if d.st.Status == StatusCompleted && !d.inSetLocked() {
		d.mu.Unlock()
		d.xmu.Unlock()
		return errors.New("the download is complete")
	}
	fixed := d.keepFixedLocked()
	if !fixed {
		if d.st.Folder == nil {
			d.st.Folder = &Folder{}
		}
		d.st.Folder.Keep, d.st.Folder.Keeping = k, ""
	}
	d.mu.Unlock()
	d.xmu.Unlock()
	if fixed {
		//lint:ignore ST1005 shown to the user as is, like the pause messages
		return errors.New(lockedNote)
	}
	d.env.Changed(d)
	return nil
}

// keepDefault is the archiveKeeping setting, KeepAuto for a download with no
// engine (a test's).
func (d *Download) keepDefault() string {
	if d.env == nil {
		return KeepAuto
	}
	return d.env.ArchiveKeeping()
}

// keptByAutoLocked: Automatic keeps the archive for now, so falling short of
// room makes it free the archive instead (Extraction.mayFree), not pause.
func (d *Download) keptByAutoLocked() bool {
	f, def := d.st.Folder, d.keepDefault()
	return d.folderOnLocked() && f.keepChoice(def) == KeepAuto && f.keepNow(def) == KeepUntil
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
		f := d.st.Folder
		if !d.inSetLocked() || !d.folderOnLocked() || f.locked() {
			d.mu.Unlock()
			return errors.New("the download is complete")
		}
		// A part already fetched takes its Set's folder. Its .part goes there
		// too if it can (not to another drive, not while the Set reads it):
		// the Set finds it either way, and deletes it once extracted.
		f.Into = dir
		if !strings.EqualFold(d.st.Dir, dir) && os.MkdirAll(dir, 0o755) == nil &&
			os.Rename(d.partPath(), filepath.Join(dir, d.st.Filename+partSuffix)) == nil {
			d.st.Dir = dir
		}
		d.mu.Unlock()
		d.env.Changed(d)
		return nil
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
		f.On = archive.Extractable(d.st.Filename) && d.st.SHA256 == "" && d.st.MD5 == ""
	}
	switch {
	case !f.On:
		return
	case !archive.Extractable(d.st.Filename):
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
	if d.inSetLocked() {
		return true // a part completes once fetched; its Set extracts it
	}
	return !d.folderOnLocked() || d.fold != nil && d.fold.finished
}

// folderRun is a download's own Extraction while the download runs, on its
// own goroutine. What Details shows of it is copied out under d.mu after
// each step, as the Extraction takes no lock.
type folderRun struct {
	ex       *Extraction
	arc      *archive.Archive // ex's list of files, once read (guarded by d.mu)
	finished bool             // every Entry done (guarded by d.mu)
	idle     bool             // the last look found nothing to extract: it waits for the download (guarded by d.mu)
	again    bool             // a large Entry has more bytes on disk to extract now (folderLoop only)
	keep     bool             // stopping parks it instead of closing it (guarded by d.mu)
	paused   error            // why the step pauses the download: an Entry came out wrong twice (guarded by d.mu)
	passed   int64            // bytes of the archive extracted, freed or kept (guarded by d.mu)
	file     *os.File         // the .part, open while parked

	cancel context.CancelFunc
	done   chan struct{}
}

// afterExtract is called after each extraction that freed bytes, with the
// .part and how many of its bytes have been downloaded; tests set it.
var afterExtract func(f *os.File, onDisk int64)

// startFolderLocked starts extracting, with the file open and the mode on.
func (d *Download) startFolderLocked() {
	if d.fold != nil || !d.folderOnLocked() || d.file == nil || !d.plan.splittable {
		return
	}
	if d.inSetLocked() {
		return // a part of a split archive: its Set extracts it
	}
	// Not from ctx: stopFolder cancels it, after saying whether to park it.
	fctx, cancel := context.WithCancel(context.Background())
	if p := d.parked; p != nil {
		// On where the last run stopped: its Archive is read, so nothing is reclaimed.
		d.parked = nil
		p.cancel, p.done, p.keep, p.again = cancel, make(chan struct{}), false, false
		d.fold = p
		d.plan.bounds = entryBounds(p.arc)
		d.wantTailLocked(p.ex.need)
		go d.folderLoop(fctx, p)
		return
	}
	ex := &Extraction{name: d.st.Filename, who: "download " + d.st.ID, file: archive.Sparse(d.file),
		parts: archive.NewParts([]int64{d.st.Size}), head: archive.HeadLen, mark: d.markLocked(),
		need: min(archive.TailGuess, d.st.Size), tail: -1}
	fr := &folderRun{ex: ex, cancel: cancel, done: make(chan struct{})}
	d.fold = fr
	d.wantTailLocked(ex.need)
	go d.folderLoop(fctx, fr)
}

// RestartCost is the bytes an engine restart now would download again: those
// freed of a large Entry under way, whose decompressor lives only in memory,
// also while parked. 0 for a part of a Set (see Set.RestartCost).
func (d *Download) RestartCost() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	if fr := cmp.Or(d.fold, d.parked); fr != nil {
		return fr.ex.streaming.Load()
	}
	return 0
}

// stopFolder stops extracting and waits until it has, so the file can close.
// With keep, an extraction under way is parked in d.parked instead.
func (d *Download) stopFolder(keep bool) {
	d.mu.Lock()
	fr := d.fold
	if fr != nil {
		fr.keep = keep
	}
	d.mu.Unlock()
	if fr != nil {
		fr.cancel()
		<-fr.done
	}
}

// dropParked closes an extraction parked while stopped, and its .part: a
// large Entry under way starts over on the next run.
// shortcut: a parked decoder holds its dictionary in memory for as long as the
// download stays paused; drop the oldest if many paused ones add up.
func (d *Download) dropParked() {
	d.mu.Lock()
	p := d.parked
	d.parked = nil
	d.mu.Unlock()
	if p == nil {
		return
	}
	p.ex.close()
	if p.file != nil {
		p.file.Close()
	}
}

// wantTailLocked has the archive's last need bytes fetched before anything
// else when they aren't on disk yet. The Extraction asks for them too, but
// only once it runs: the first claims come before that.
func (d *Download) wantTailLocked(need int64) {
	if start := d.st.Size - need; !onDisk(d.plan.ranges(), start, d.st.Size) {
		d.plan.cut(start)
		d.plan.prefer = start
	}
}

func (d *Download) folderLoop(ctx context.Context, fr *folderRun) {
	defer func() {
		d.mu.Lock()
		park := fr.keep && fr.ex.x != nil && !fr.finished
		if park {
			d.parked = fr
		}
		if d.fold == fr {
			d.fold = nil
		}
		d.mu.Unlock()
		if !park {
			// A large Entry under way is dropped: the next run puts its
			// freed bytes back in the plan and extracts it from its front.
			fr.ex.close()
		}
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

// folderStep steps the Extraction with what is on disk now. more is false
// once the mode is off.
func (d *Download) folderStep(ctx context.Context, fr *folderRun) (more bool, err error) {
	// Not while SetFolder or SetHashes change what may be freed.
	d.xmu.Lock()
	defer d.xmu.Unlock()
	d.mu.Lock()
	on := d.folderOnLocked() && d.set == nil // it may have turned out a part of a Set, which extracts it
	missing := d.plan.ranges()
	d.mu.Unlock()
	if !on {
		return false, nil
	}
	am := make([]archive.Range, len(missing))
	for i, r := range missing {
		am[i] = archive.Range{Start: r.Start, End: r.End}
	}
	again, err := fr.ex.step(ctx, []archivePart{d}, am)

	d.mu.Lock()
	read := fr.arc == nil && fr.ex.arc != nil
	fr.arc, fr.finished, fr.idle, fr.again = fr.ex.arc, fr.ex.finished, fr.ex.idle, again
	fr.passed = fr.ex.passed
	if read && d.folderOnLocked() {
		d.plan.bounds = entryBounds(fr.arc)
	}
	onDisk := d.st.Size - d.plan.remaining()
	file := d.file
	paused := fr.paused
	fr.paused = nil
	d.mu.Unlock()
	if fr.ex.freed && afterExtract != nil {
		afterExtract(file, onDisk)
	}

	var u *archive.Unsupported
	switch {
	case errors.As(err, &u):
		return d.folderFallback(u)
	case err != nil && fr.arc == nil:
		return false, fmt.Errorf("reading the archive's list of files: %w", err)
	case paused != nil:
		return true, paused
	}
	return true, err
}

var errFolderRemoved = &pauseError{msg: "Paused: the folder was removed"}

// folderFallback turns the mode off for an archive it can't handle: nothing
// has been freed, so the download goes on as a normal one.
func (d *Download) folderFallback(u *archive.Unsupported) (bool, error) {
	d.mu.Lock()
	d.folderOffLocked()
	d.st.Folder.On, d.st.Folder.Note = false, u.Reason+", so it is downloaded as a "+strings.ToLower(filepath.Ext(d.st.Filename))
	d.mu.Unlock()
	log.Printf("download %s: not as a folder: %s", d.st.ID, u.Reason)
	d.env.Changed(d)
	return false, nil
}

// thousands writes n with commas: 1,280.
func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
