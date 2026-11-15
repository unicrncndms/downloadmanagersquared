package download

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"downloadmanagersquared/engine/internal/archive"
)

// A download's own Extraction while it runs (Download as folder of one file; a
// split archive's parts are read by their Set instead). Split from folder.go.

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
	d.changed()
	return false, nil
}
