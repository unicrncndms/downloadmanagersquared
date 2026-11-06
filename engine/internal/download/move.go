package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"downloadmanagersquared/engine/internal/winutil"
)

// Moving a Download as folder's extraction folder while it downloads (ticket
// 07). The output folder and the .part go together into the new folder: on
// the same drive by renaming them, on another by copying them (only the
// .part's allocated bytes) and deleting the old copies. A move happens with
// nothing open: a running download restarts around it, keeping its Slot, and
// does it first thing in its next run.

// sameDrive: a and b are on one volume, so moving between them is a rename.
// Tests make it false to move between "drives" on one.
var sameDrive = func(a, b string) bool {
	return strings.EqualFold(filepath.VolumeName(a), filepath.VolumeName(b))
}

// duringMove is called once a move between drives has copied everything,
// before the old copies go; tests set it.
var duringMove func(oldPart, newPart string)

const (
	inUseNote = "Couldn't move: a file in it is in use"
	movedTomb = ".dms-moved" // an old output folder on its way out after a copy
)

// moveToLocked starts moving the extraction to dir, with d.mu held; it
// unlocks. A running download restarts and moves first thing; a stopped one
// moves now when it is a rename, else when it next runs (a copy can take
// longer than a request).
func (d *Download) moveToLocked(dir string) error {
	f := d.st.Folder
	if strings.EqualFold(dir, f.Into) {
		// Where it already is: a pending move is called off.
		f.Moving, f.MoveError = "", ""
		d.mu.Unlock()
		d.env.Changed(d)
		return nil
	}
	f.Moving, f.MoveError = dir, ""
	running := d.running
	copying := d.copyingLocked(dir)
	d.mu.Unlock()
	switch {
	case running:
		d.restart()
	case !copying:
		err := d.relocate(context.Background())
		d.env.Changed(d)
		return err
	}
	d.env.Changed(d)
	return nil
}

// copyingLocked: moving into dir copies something, the folder or the .part,
// to another drive.
func (d *Download) copyingLocked(dir string) bool {
	f := d.st.Folder
	return f.Out != "" && !sameDrive(f.Out, dir) || d.st.Allocated && !sameDrive(d.partPath(), dir)
}

// relocate does a pending move, or puts a .part left on another drive than
// the folder it extracts into next to it. Nothing may have the files open.
// A move that fails leaves everything where it was and says why in
// Folder.MoveError; one stopped partway is tried again next time.
func (d *Download) relocate(ctx context.Context) error {
	// One move at a time: a stopped download's rename and a run starting
	// after a Resume would otherwise both move the same files.
	d.mvmu.Lock()
	defer d.mvmu.Unlock()
	d.mu.Lock()
	f := d.st.Folder
	if !d.folderOnLocked() {
		d.mu.Unlock()
		return nil
	}
	target := f.Moving
	if target == "" && f.Out == "" && d.moveNeededLocked() {
		target = f.Into // the .part goes next to where it extracts
	}
	if target == "" {
		d.mu.Unlock()
		return nil
	}
	m := &move{
		target: target, oldOut: f.Out, oldPart: d.partPath(), partName: d.st.Filename, hasPart: d.st.Allocated,
		moveOut: f.MoveOut, movePart: f.MovePart,
	}
	copying := d.copyingLocked(target)
	d.moving = copying
	d.mu.Unlock()
	if copying {
		d.env.Changed(d)
	}

	err := d.doMove(ctx, m)

	d.mu.Lock()
	d.moving = false
	if f = d.st.Folder; f == nil {
		// Gone meanwhile (the mode can't go off once anything is freed, but
		// a move of only the .part can happen before).
		d.mu.Unlock()
		return err
	}
	switch {
	case err == nil:
		f.Into, f.Moving, f.MoveError, f.MoveOut, f.MovePart = target, "", "", "", ""
		if m.newOut != "" {
			f.Out = m.newOut
		}
		d.st.Dir = target
		if m.newPart != "" {
			d.st.Filename = strings.TrimSuffix(filepath.Base(m.newPart), partSuffix)
		}
	case ctx.Err() != nil:
		// Stopped partway: its copies are gone (undo), and it is tried
		// again when the download next runs.
		f.MoveOut, f.MovePart = "", ""
	default:
		f.Moving, f.MoveError, f.MoveOut, f.MovePart = "", err.Error(), "", ""
		log.Printf("download %s: %v", d.st.ID, err)
	}
	d.mu.Unlock()
	d.env.Changed(d)
	if err == nil {
		m.removeOld()
	}
	return err
}

// move is one move of the output folder and the .part into target.
type move struct {
	target          string
	oldOut, oldPart string // "" / unused when there is none
	partName        string // the .part's name without its suffix
	hasPart         bool
	moveOut         string // where an earlier try copied to, from the saved state
	movePart        string
	newOut, newPart string
	copiedOut       bool
	copiedPart      bool
	renamedOut      bool
	renamedPart     bool
}

func moveError(err error) error {
	// Shown to the user as they are, like the pause messages.
	if inUse(err) {
		return errors.New(inUseNote) //lint:ignore ST1005 a sentence for the user
	}
	return fmt.Errorf("Couldn't move: %w", err) //lint:ignore ST1005 a sentence for the user
}

func (d *Download) doMove(ctx context.Context, m *move) error {
	if m.adopt() {
		return nil
	}
	// What an earlier try left half-copied.
	if m.moveOut != "" {
		os.RemoveAll(m.moveOut)
	}
	if m.movePart != "" {
		os.Remove(m.movePart)
	}
	if err := os.MkdirAll(m.target, 0o755); err != nil {
		return moveError(err)
	}
	m.copiedOut = m.oldOut != "" && !sameDrive(m.oldOut, m.target)
	m.copiedPart = m.hasPart && !sameDrive(m.oldPart, m.target)
	if m.copiedOut {
		// Copying would work around a file held open, but the old folder
		// couldn't be deleted after: find out first, as a rename would.
		if err := os.Rename(m.oldOut, m.oldOut+movedTomb); err != nil {
			return moveError(err)
		}
		if err := os.Rename(m.oldOut+movedTomb, m.oldOut); err != nil {
			return moveError(err)
		}
	}
	var need int64
	if m.copiedOut {
		need += treeSize(m.oldOut)
	}
	if m.copiedPart {
		need += allocatedSize(m.oldPart)
	}
	if need > 0 {
		if err := checkSpace(m.target, need); err != nil {
			return errors.New("Couldn't move: " + strings.TrimPrefix(err.Error(), "Paused: "))
		}
	}
	if m.oldOut != "" {
		m.newOut = filepath.Join(m.target, uniqueName(m.target, filepath.Base(m.oldOut), ""))
	}
	if m.hasPart {
		m.newPart = filepath.Join(m.target, uniqueName(m.target, m.partName, "", partSuffix)+partSuffix)
	}
	if m.copiedOut || m.copiedPart {
		// Saved before copying, so a crash midway knows what to clear up.
		d.mu.Lock()
		if m.copiedOut {
			d.st.Folder.MoveOut = m.newOut
		}
		if m.copiedPart {
			d.st.Folder.MovePart = m.newPart
		}
		d.mu.Unlock()
		d.env.Changed(d)
	}

	// The .part first: a folder held open is what usually stops a move, and
	// a .part is simply put back.
	if m.hasPart {
		var err error
		if m.copiedPart {
			err = copySparse(ctx, m.oldPart, m.newPart)
		} else if err = os.Rename(m.oldPart, m.newPart); err == nil {
			m.renamedPart = true
		}
		if err != nil {
			m.undo()
			return moveError(err)
		}
	}
	if m.oldOut != "" {
		var err error
		if m.copiedOut {
			err = copyTree(ctx, m.oldOut, m.newOut)
		} else if err = os.Rename(m.oldOut, m.newOut); err == nil {
			m.renamedOut = true
		}
		if err != nil {
			m.undo()
			return moveError(err)
		}
	}
	if duringMove != nil && (m.copiedOut || m.copiedPart) {
		duringMove(m.oldPart, m.newPart)
	}
	return nil
}

// adopt takes an earlier try's copies as they are when it got as far as
// deleting the old copies (a crash before its state was saved): the copy was
// complete by then.
func (m *move) adopt() bool {
	if m.moveOut == "" && m.movePart == "" {
		return false
	}
	gone := m.oldOut != "" && m.moveOut != "" && !exists(m.oldOut) ||
		m.hasPart && m.movePart != "" && !exists(m.oldPart)
	if !gone || m.moveOut != "" && !exists(m.moveOut) || m.movePart != "" && !exists(m.movePart) {
		return false
	}
	m.newOut, m.newPart = m.oldOut, ""
	if m.moveOut != "" {
		m.newOut, m.copiedOut = m.moveOut, true
	}
	if m.hasPart {
		m.newPart = m.oldPart
		if m.movePart != "" {
			m.newPart, m.copiedPart = m.movePart, true
		}
	}
	return true
}

// undo puts back what a failed move did.
func (m *move) undo() {
	switch {
	case m.renamedOut:
		os.Rename(m.newOut, m.oldOut)
	case m.copiedOut && m.newOut != "":
		os.RemoveAll(m.newOut)
	}
	switch {
	case m.renamedPart:
		os.Rename(m.newPart, m.oldPart)
	case m.copiedPart && m.newPart != "":
		os.Remove(m.newPart)
	}
}

// removeOld deletes what was copied once the move is saved. The old folder
// is renamed out of the way first, so a crash midway leaves no half-deleted
// folder that could be taken for the real one.
func (m *move) removeOld() {
	if m.copiedOut {
		tomb := m.oldOut + movedTomb
		if exists(m.oldOut) {
			if err := os.Rename(m.oldOut, tomb); err != nil {
				log.Printf("leaving the old folder %s: %v", m.oldOut, err)
				tomb = ""
			}
		}
		if m.copiedPart {
			removeLogged(m.oldPart, os.Remove)
		}
		if tomb != "" {
			removeLogged(tomb, os.RemoveAll)
		}
		return
	}
	if m.copiedPart {
		removeLogged(m.oldPart, os.Remove)
	}
}

func removeLogged(path string, remove func(string) error) {
	if err := remove(path); err != nil {
		log.Printf("leaving %s after a move: %v", path, err)
	}
}

func inUse(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

// treeSize is the bytes of the files under dir.
func treeSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if err == nil && e.Type().IsRegular() {
			if fi, err := e.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// allocatedSize is the bytes of a sparse file that take up space.
func allocatedSize(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	rs, err := winutil.AllocatedRanges(f)
	if err != nil {
		fi, err := f.Stat()
		if err != nil {
			return 0
		}
		return fi.Size()
	}
	var n int64
	for _, r := range rs {
		n += r.End - r.Start
	}
	return n
}

// copyTree copies the folder src to dst, which mustn't exist: files with
// their modification times and read-only flags, and empty folders.
func copyTree(ctx context.Context, src, dst string) error {
	type dirTime struct {
		path string
		t    time.Time
	}
	var dirs []dirTime
	err := filepath.WalkDir(src, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		fi, err := e.Info()
		if err != nil {
			return err
		}
		switch {
		case e.IsDir():
			dirs = append(dirs, dirTime{to, fi.ModTime()})
			return os.MkdirAll(to, 0o755)
		case e.Type().IsRegular():
			return copyFile(ctx, p, to, fi)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- { // deepest first: filling a folder changes its time
		os.Chtimes(dirs[i].path, dirs[i].t, dirs[i].t)
	}
	return nil
}

func copyFile(ctx context.Context, src, dst string, fi fs.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, ctxReader{ctx, in})
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	os.Chtimes(dst, fi.ModTime(), fi.ModTime())
	if fi.Mode().Perm()&0o200 == 0 {
		os.Chmod(dst, 0o444) // read-only
	}
	return nil
}

// copySparse copies the sparse file src to dst, which mustn't exist: only
// its allocated ranges are read and written, the rest stays a hole.
func copySparse(ctx context.Context, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	rs, err := winutil.AllocatedRanges(in)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	err = func() error {
		if err := winutil.SetSparse(out, true); err != nil {
			return err
		}
		if err := out.Truncate(fi.Size()); err != nil {
			return err
		}
		for _, r := range rs {
			w := io.NewOffsetWriter(out, r.Start)
			if _, err := io.Copy(w, ctxReader{ctx, io.NewSectionReader(in, r.Start, r.End-r.Start)}); err != nil {
				return err
			}
		}
		return out.Sync()
	}()
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
	}
	return err
}

// moveRefetchLocked is what a move now would download again: the bytes freed of
// Entries not done, a large Entry under way, which starts over after it.
func (d *Download) moveRefetchLocked() int64 {
	fr := d.fold
	if fr == nil || fr.arc == nil {
		return 0
	}
	f := d.st.Folder
	done := map[int64]bool{}
	for _, s := range f.Done {
		done[s] = true
	}
	var n int64
	for _, e := range fr.arc.Entries {
		if done[e.Start] {
			continue
		}
		for _, r := range f.Freed {
			n += max(0, min(r.End, e.End)-max(r.Start, e.Start))
		}
	}
	return n
}
