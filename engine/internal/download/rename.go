package download

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"downloadmanagersquared/engine/internal/archive"
)

// Renaming an unfinished download's file by hand (ticket 05). The .part and
// the name the finished file gets follow. Only a download that isn't running
// takes it, as with SetDir: the engine pauses a running one around the call.

// Why a file name can't be changed: the name belongs to something else.
const (
	noRenameFolder = "A download as folder is named by its folder, so its file name can't be changed."
	noRenameSet    = "A part of a split archive keeps its name: the other parts are found by it."
)

// renameBlock says why a download's file can't be renamed, "" when it can.
func renameBlock(folderOn, inSet bool) string {
	switch {
	case folderOn:
		return noRenameFolder
	case inSet:
		return noRenameSet
	}
	return ""
}

// CleanFileName is a name the user typed made fit for a Windows file, by
// SanitizeFilename's rules (characters Windows refuses, reserved names). The
// spaces around it go first, so a pasted name's tab isn't kept as "_". "" when
// nothing is left.
func CleanFileName(name string) string { return SanitizeFilename(strings.TrimSpace(name)) }

// Rename gives the download's file a new name: the .part is renamed and the
// finished file will take the name. A name another file or .part in the
// folder has is numbered (name (1).ext). A download as folder, a part of a
// split archive, a running or a finished download is refused.
func (d *Download) Rename(name string) error {
	name = CleanFileName(name)
	if name == "" {
		return errors.New("enter a file name")
	}
	d.mvmu.Lock() // not while a run's relocate or a move has the file
	defer d.mvmu.Unlock()
	d.mu.Lock()
	f := d.st.Folder
	folderOn := d.folderOnLocked() || f != nil && (f.On || f.Auto && archive.Extractable(d.st.Filename))
	why := renameBlock(folderOn, d.inSetLocked())
	switch {
	case d.st.Status == StatusCompleted:
		d.mu.Unlock()
		return errors.New("the download is complete")
	case why != "":
		d.mu.Unlock()
		return errors.New(why)
	case d.running:
		d.mu.Unlock()
		return errors.New("pause the download first")
	case name == d.st.Filename:
		d.mu.Unlock()
		return nil
	}
	if d.parked != nil {
		d.mu.Unlock()
		d.dropParked() // its .part can't be renamed while open
		d.mu.Lock()
	}
	dir, old, allocated := d.st.Dir, d.st.Filename, d.st.Allocated
	d.mu.Unlock()
	if !strings.EqualFold(name, old) { // only the case changes: the name is its own
		name = uniqueName(dir, name, "", partSuffix)
	}
	if allocated {
		if err := os.Rename(filepath.Join(dir, old+partSuffix), filepath.Join(dir, name+partSuffix)); err != nil {
			return fmt.Errorf("couldn't rename: %w", err)
		}
	}
	d.mu.Lock()
	d.st.Filename = name
	d.mu.Unlock()
	d.changed()
	return d.save()
}
