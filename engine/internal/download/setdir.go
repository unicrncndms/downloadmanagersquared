package download

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

// SetDir moves an unfinished download's partial file into dir and makes it the
// folder the file is saved in. Only a download that isn't running takes it
// (paused, queued or failed): a running one has the file open, so the UI asks
// for a pause first. A Download as folder moves with SetFolderInto instead.
// The move is move.go's: a rename on one drive, a copy of the allocated ranges
// across drives; one that fails changes nothing.
func (d *Download) SetDir(dir string) error {
	if !filepath.IsAbs(dir) || filepath.VolumeName(dir) == "" {
		return errors.New("not a folder: " + dir)
	}
	dir = filepath.Clean(dir)
	d.mvmu.Lock() // not while a run's relocate or another move has the file
	defer d.mvmu.Unlock()
	d.mu.Lock()
	switch {
	case d.st.Status == StatusCompleted:
		d.mu.Unlock()
		return errors.New("the download is complete")
	case d.folderOnLocked() || d.st.Folder != nil && d.st.Folder.On:
		d.mu.Unlock()
		return errors.New("a download as folder moves with its Extract into folder")
	case d.running:
		d.mu.Unlock()
		return errors.New("pause the download first")
	case strings.EqualFold(dir, d.st.Dir):
		d.mu.Unlock()
		return nil
	}
	if d.parked != nil {
		d.mu.Unlock()
		d.dropParked() // its .part can't move while open
		d.mu.Lock()
	}
	m := &move{target: dir, oldPart: d.partPath(), partName: d.st.Filename, hasPart: d.st.Allocated}
	d.mu.Unlock()
	if err := d.doMove(context.Background(), m); err != nil {
		return err
	}
	d.mu.Lock()
	d.st.Dir = dir
	if m.newPart != "" {
		d.st.Filename = strings.TrimSuffix(filepath.Base(m.newPart), partSuffix)
	}
	d.mu.Unlock()
	d.changed()
	if err := d.save(); err != nil {
		return err // the old copy stays, as in relocate
	}
	m.removeOld()
	return nil
}
