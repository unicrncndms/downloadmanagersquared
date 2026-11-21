package download

import (
	"cmp"
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

	// The last run ended because the extraction failed, not the download:
	// the card says "Extraction failed", and Resume tries it again.
	ExtractFailed bool `json:"extractFailed,omitempty"`
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

	ExtractFailed bool `json:"extractFailed,omitempty"` // the last run ended on an extraction error
}

func (f *Folder) summary() *FolderSummary {
	if f == nil {
		return nil
	}
	return &FolderSummary{On: f.On, Into: f.Into, Out: f.Out, Locked: f.locked(), Files: f.Files, Of: f.Of,
		Renamed: slices.Clone(f.Renamed), Note: f.Note, Ask: f.Ask, Moving: f.Moving, MoveError: f.MoveError, ExtractFailed: f.ExtractFailed}
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
	d.changed()
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
		if x := fr.ex.live.Load(); x != nil && s.Of > 0 { // Of 0: a RAR, whose count is only safe to read between steps
			s.Files, s.Of = x.Progress()
		}
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

// FolderInto is the folder a Download as folder extracts into, "" when it is
// not one yet (or has none).
func (d *Download) FolderInto() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if f := d.st.Folder; f != nil && f.On {
		return f.Into
	}
	return ""
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
	d.changed()
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
	d.changed()
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
		if set := d.set; set != nil && !strings.EqualFold(d.st.Dir, dir) {
			d.mu.Unlock()
			defer set.Hold()() // its handle on the .part would stop the rename
			d.dropParked()
			d.mu.Lock()
		}
		if !strings.EqualFold(d.st.Dir, dir) && os.MkdirAll(dir, 0o755) == nil &&
			os.Rename(d.partPath(), filepath.Join(dir, d.st.Filename+partSuffix)) == nil {
			d.st.Dir = dir
		}
		d.mu.Unlock()
		d.changed()
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
	d.changed()
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

// thousands writes n with commas: 1,280.
func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// completeFolder ends a download whose bytes are a Download as folder's
// archive, for complete: the archive is read as it comes, so there is no file
// to check and name, unless it is kept afterwards (keep). handled: complete
// has nothing more to do. f is the .part's handle, closed here.
func (d *Download) completeFolder(f *os.File, part, name string, folder, keep, fromServer bool, set *Set) (handled bool) {
	if _, _, ok := Splittable(name); (ok || set != nil) && folder {
		// A part of a split archive: its Set reads the .part, frees it and
		// deletes it once extracted.
		if f != nil {
			f.Close()
		}
		if set != nil && set.finished.Load() {
			d.settlePart(part, keep)
		}
		return true
	}
	if folder && !keep {
		// Every Entry is extracted and checked: the archive itself isn't kept.
		if f != nil {
			f.Close()
		}
		if err := os.Remove(part); err != nil {
			log.Printf("download %s: removing the partial file: %v", d.st.ID, err)
		}
		if fromServer {
			d.mu.Lock()
			d.st.Folder.Note = serverMD5
			d.mu.Unlock()
		}
		return true
	}
	return false
}

// settlePart ends a finished Set's part: its .part is named after all when the
// archive is kept afterwards (ADR 0007), else deleted.
func (d *Download) settlePart(part string, keep bool) {
	if keep {
		d.keepPart(part)
	} else if err := os.Remove(part); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("download %s: removing the part: %v", d.ID(), err)
	}
}
