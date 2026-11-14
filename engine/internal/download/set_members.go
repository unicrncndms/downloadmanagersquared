package download

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"downloadmanagersquared/engine/internal/archive"
)

// A Set's members: which downloads are parts of one, and their part numbers.
// Split from set.go.

// Splittable says whether a file is a part of a split archive that a Set
// extracts: the set's name and the part's number.
// A plain x.rar is only part 1 of a set beside an x.r00 (see setOf), and
// x.rar.001 parts are a cut file, not volumes: neither is Splittable.
func Splittable(name string) (set string, part int, ok bool) {
	set, part, ok = archive.PartOf(name)
	if !ok || part < 1 {
		return "", 0, false
	}
	switch strings.ToLower(filepath.Ext(set)) {
	case ".7z":
		ok = !strings.EqualFold(filepath.Ext(name), ".7z")
	case ".rar":
		ok = !strings.EqualFold(name, set) && !strings.HasPrefix(strings.ToLower(name), strings.ToLower(set)+".")
	case ".zip":
		ok = true // x.z01… or x.zip.001…; a plain x.zip is LastPart, below 1
	default:
		ok = false
	}
	if !ok {
		return "", 0, false
	}
	return set, part, true
}

// setOf is the Set a download belongs to: its key ("" for none), the
// archive's name and the part's number. lone is a plain x.rar or x.zip: part
// 1 or the last part of the set only when other parts of it (x.r00…, x.z01…)
// are there.
func setOf(d *Download) (key, set string, part int, lone bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	dir := d.setDirLocked()
	if set, part, ok := Splittable(d.st.Filename); ok {
		return archive.SetKey(dir, set), set, part, false
	}
	switch strings.ToLower(filepath.Ext(d.st.Filename)) {
	case ".rar":
		return archive.SetKey(dir, d.st.Filename), d.st.Filename, 1, true
	case ".zip":
		return archive.SetKey(dir, d.st.Filename), d.st.Filename, archive.LastPart, true
	}
	return "", "", 0, false
}

// setDirLocked is the folder that says which Set a part is in: the one it
// extracts into as a Download as folder, else the one it is saved in. Not
// where its .part happens to be: that moves to the folder it extracts into
// only when the part next runs, and a part already fetched never runs again,
// so parts given one folder at once would be in two Sets, for good.
func (d *Download) setDirLocked() string {
	if f := d.st.Folder; f != nil && (f.On || f.Auto) && f.Into != "" {
		return filepath.Clean(f.Into)
	}
	return filepath.Clean(d.st.Dir)
}

func NewSet(key, set string) *Set {
	ext := strings.ToLower(filepath.Ext(set))
	return &Set{Key: key, set: set, rar: ext == ".rar", zip: ext == ".zip", members: map[int]*Download{}}
}

// inSetLocked: its Set extracts it, not the download itself.
func (d *Download) inSetLocked() bool {
	_, _, ok := Splittable(d.st.Filename)
	return ok || d.set != nil
}

// SetMembers makes these the Set's parts, by part number; the downloads it
// had that aren't among them leave it.
func (s *Set) SetMembers(parts map[int]*Download) {
	s.mu.Lock()
	old := s.members
	s.members = parts
	s.mu.Unlock()
	for _, d := range old {
		d.mu.Lock()
		if d.set == s {
			d.set = nil
		}
		d.mu.Unlock()
	}
	for _, d := range parts {
		d.mu.Lock()
		d.set = s
		d.mu.Unlock()
	}
}

// Mates is d with the other parts of its Set: Download as folder is one
// switch and one folder for all of them. Only d when it is in no Set.
func (d *Download) Mates() []*Download {
	d.mu.Lock()
	s := d.set
	d.mu.Unlock()
	out := []*Download{d}
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.members {
		if m != d {
			out = append(out, m)
		}
	}
	return out
}

// ApplyToMates gives the other parts of d's Set the switch, folder and archive
// keeping d took (nil: unchanged), so the Set has one choice. A part that can't
// (it is complete, or locked) stays; the errors come back joined for the log.
func (d *Download) ApplyToMates(on *bool, into, keep *string) error {
	var errs []error
	for _, m := range d.Mates()[1:] {
		if on != nil {
			errs = append(errs, m.SetFolder(*on))
		}
		if into != nil {
			errs = append(errs, m.SetFolderInto(*into))
		}
		if keep != nil {
			errs = append(errs, m.SetArchiveKeeping(*keep))
		}
	}
	return errors.Join(errs...)
}

// discard deletes the .part of a part removed with its files. The loop does
// it: it may have the file open, and Windows deletes no open file.
func (s *Set) discard(part string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.done == nil {
		os.Remove(part)
		return
	}
	s.trash = append(s.trash, part)
}

// empty deletes what discard left, with the run's files closed: the run is
// built again from the parts' record, as after a restart.
func (s *Set) empty(run *setRun, stop bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = s.stopped || stop
	if len(s.trash) == 0 {
		return
	}
	if run.parts != nil {
		run.close()
	}
	*run = setRun{}
	for _, p := range s.trash {
		os.Remove(p)
	}
	s.trash = nil
}
