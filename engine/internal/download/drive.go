package download

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// drivePause is the pause for err when it comes from dir's drive being full
// or gone, nil otherwise: neither loses anything, so the download waits
// instead of failing. need is what is still to download, 0 when unknown. A
// download paused for a missing drive resumes by itself (DriveBack).
func drivePause(err error, dir string, need int64) *pauseError {
	if err == nil {
		return nil
	}
	drive, name := filepath.VolumeName(dir), driveName(dir)
	switch {
	case errors.Is(err, errDiskFull) || errors.Is(err, errHandleFull):
		msg := "Paused: " + name + " is full"
		if need > 0 {
			msg += fmt.Sprintf(" (%s still to download)", human(need))
		}
		return &pauseError{msg: msg + ": free some space, then resume"}
	case drive != "" && (driveGone(err) || !driveThere(drive)):
		return &pauseError{msg: "Paused: " + name + " is not available. It resumes by itself once it is back.", drive: drive}
	}
	return nil
}

// driveName is how a message names dir's drive: "drive E:", or a network
// folder by its share.
func driveName(dir string) string {
	drive := filepath.VolumeName(dir)
	switch {
	case strings.HasPrefix(drive, `\\`):
		return "the network folder " + drive
	case drive == "":
		return "the disk"
	}
	return "drive " + drive
}

func driveGone(err error) bool {
	return errors.Is(err, errNotReady) || errors.Is(err, errNetGone) || errors.Is(err, errBadNetPath) || errors.Is(err, errNetDeleted)
}

// driveThere is a var so tests can pull a drive out.
var driveThere = func(drive string) bool {
	_, err := os.Stat(drive + `\`)
	return err == nil
}

// driveCheckEvery is how often a download waiting for its drive looks for it:
// a network drive that is gone can take seconds to say so.
const driveCheckEvery = 10 * time.Second

// DriveBack is whether the download paused because its drive was gone and the
// drive is back now, so it can resume. It looks at most every driveCheckEvery.
func (d *Download) DriveBack() bool {
	d.mu.Lock()
	drive := d.st.WaitDrive
	if drive == "" || d.st.Status != StatusPaused || time.Since(d.driveChecked) < driveCheckEvery {
		d.mu.Unlock()
		return false
	}
	d.driveChecked = time.Now()
	d.mu.Unlock()
	return driveThere(drive)
}
