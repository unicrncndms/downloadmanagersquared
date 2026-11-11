package download

import (
	"errors"
	"os"
	"time"

	"downloadmanagersquared/engine/internal/paths"
)

// Resume lets a stopped download run again: back in the queue.
func (d *Download) Resume() { d.Stop(StatusQueued) }

// Discard stops the download for good. With deleteFile, an unfinished file
// goes, and a Download as folder's folder with it; a completed one is the user's.
func (d *Download) Discard(deleteFile bool) {
	finished := d.Status() == StatusCompleted
	d.Stop(StatusPaused)
	d.dropParked()
	d.mu.Lock()
	set, inSet := d.set, d.inSetLocked()
	d.mu.Unlock()
	// A fetched part of a split archive still has its .part, for its Set.
	if !deleteFile || finished && !inSet {
		return
	}
	d.mu.Lock()
	part, out, moveOut, movePart := d.partPath(), "", "", ""
	if f := d.st.Folder; f != nil {
		// With the copies a move between drives left halfway, as after a crash.
		out, moveOut, movePart = f.Out, f.MoveOut, f.MovePart
	}
	d.mu.Unlock()
	if set != nil {
		set.discard(part)
	} else {
		os.Remove(part)
	}
	if finished {
		return // what is extracted stays, as a finished download's file does
	}
	for _, dir := range []string{out, moveOut} {
		if dir != "" {
			os.RemoveAll(dir)
		}
	}
	if movePart != "" {
		os.Remove(movePart)
	}
}

// Yields is false: HTTP downloads go first in the queue.
func (d *Download) Yields() bool { return false }

// Rejoins is false: an HTTP download only runs in a slot.
func (d *Download) Rejoins() bool { return false }

// Added is when the download was added.
func (d *Download) Added() time.Time { return d.st.Added } // fixed when added: no lock needed

// Saved is the download's kind and the state that resumes it after a restart.
// The file is flushed to disk first, so the progress the state records is there
// even after a power cut.
func (d *Download) Saved() (string, any) {
	st := d.State()
	d.mu.Lock()
	f := d.file
	d.mu.Unlock()
	if f != nil {
		f.Sync() // FlushFileBuffers; fails harmlessly if the download closed it meanwhile
	}
	return "downloads", st
}

// VPN is the download's VPN mode.
func (d *Download) VPN() paths.Mode {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.st.VPN
}

// SetVPN changes which paths the download may use. A running download moves
// within a tick: connections on paths the mode no longer allows close, and
// paths it now allows are probed. Under VPN only with no tunnel up, the queue
// pauses it until one is.
func (d *Download) SetVPN(m paths.Mode) error {
	d.mu.Lock()
	if d.st.Status == StatusCompleted {
		d.mu.Unlock()
		return errors.New("the download is complete")
	}
	same := d.st.VPN == m
	d.st.VPN = m
	d.mu.Unlock()
	if !same {
		d.env.Changed(d)
	}
	return nil
}

// PauseForVPN pauses a running or queued download because, under VPN only, no
// tunnel is up; the engine queues it again once one is. A paused, finished or
// failed download is left as it is.
func (d *Download) PauseForVPN() {
	d.mu.Lock()
	ok := d.running || d.st.Status == StatusQueued
	d.mu.Unlock()
	if !ok {
		return
	}
	d.Stop(StatusPaused)
	d.mu.Lock()
	d.st.Error, d.st.WaitVPN = "Paused: no VPN tunnel is up. It resumes by itself once one is.", true
	d.mu.Unlock()
	d.env.Changed(d)
}

// WaitingForVPN is whether the download paused only because no tunnel was up.
func (d *Download) WaitingForVPN() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.st.Status == StatusPaused && d.st.WaitVPN
}
