package download

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"downloadmanagersquared/engine/internal/archive"
)

// archivePart is what an Extraction sees of a download that holds some of the
// archive's bytes. The download does its own locking behind these, so the
// Extraction never takes a part's lock.
type archivePart interface {
	// partInfo is its bytes on disk and its Folder, as they are now.
	partInfo() partInfo
	// editFolder changes its Folder under its lock; edit says whether
	// anything changed, so that it is saved.
	editFolder(edit func(f *Folder) bool)
	// preferTail has it fetch from at on first, or from nowhere first for -1.
	preferTail(at int64)
	// refetch has it fetch r again, whether r is on disk or freed.
	refetch(r Range)
	// pause stops it, with msg as why.
	pause(msg string)
	// save writes its state to disk at once.
	save() error
}

// Extraction is one archive extracted while its bytes download (CONTEXT.md):
// one download's own, as its one part, or a Set's parts laid end to end. It
// reads the archive's list of files (its tail first), waits for the password
// part 1 is asked for, names and makes the output folder, extracts what is on
// disk, and records what it did on the parts: the Entries done, those that
// came out wrong and the count of files on part 1, the bytes freed on the
// part they are in. Its owner steps it on one goroutine and decides what an
// error means; the Extraction holds no lock of its own.
type Extraction struct {
	name    string         // the archive's name, which names the output folder without its extension
	who     string         // "download <id>" or "set <name>", for the log
	file    archive.File   // the archive's bytes end to end
	parts   *archive.Parts // where each part sits in file
	head    int64          // how much of its front reading the list of files needs
	volumes bool           // a RAR in volumes: its list is read from its front (ReadVolumes)
	mark    func(path string)

	need     int64 // bytes of the tail the list of files needs; 0 until guessed from the last part
	arc      *archive.Archive
	x        *archive.Extractor
	out      string
	finished bool // every Entry is done

	// What the last step found, for the owner's progress.
	tail   int  // the part whose end it waits for, -1 for none
	asking bool // it waits for the password
	idle   bool // nothing on disk was extractable: it waits for the download
	freed  bool // bytes were freed

	passed int64     // bytes of the archive extracted, freed or kept (Extractor.Passed)
	roomAt time.Time // when Automatic, keeping the archive, last looked at the free space

	// streaming is the bytes freed of the large Entry under way, which a stop
	// now would have fetched again (Extractor.Streaming); read by RestartCost.
	streaming atomic.Int64
}

// saveFreed records rs, in the archive's offsets, as freed on the parts they
// are in, and saves those parts, before the Extractor frees them: freed bytes
// a crash forgot would be taken as on disk. Recording bytes not freed yet is
// harmless: they are fetched again.
func (e *Extraction) saveFreed(parts []archivePart, rs []archive.Range) error {
	freed := map[int][]Range{}
	for _, r := range rs {
		idx, local := e.parts.Cut(r)
		for k, i := range idx {
			freed[i] = append(freed[i], Range{local[k].Start, local[k].End})
		}
	}
	for i, rs := range freed {
		p := parts[i]
		if p == nil {
			continue
		}
		p.editFolder(func(f *Folder) bool {
			f.Freed = mergeRanges(append(f.Freed, rs...))
			return true
		})
		if err := p.save(); err != nil {
			return fmt.Errorf("saving what is freed: %w", err)
		}
	}
	return nil
}

// step does what can be done now with the archive's bytes on disk; missing
// are those that aren't, in the archive's own offsets. parts are the parts
// by index, nil for one that isn't there; part 1 is always there. more: call
// again at once.
//
// An error is the owner's to judge: an *archive.Unsupported means the
// archive can't be extracted as it downloads, and any error while the list of
// files isn't read yet (arc is nil) may be its bytes not all being there.
func (e *Extraction) step(ctx context.Context, parts []archivePart, missing []archive.Range) (more bool, err error) {
	e.tail, e.asking, e.idle, e.freed = -1, false, false, false
	if e.finished {
		return false, nil
	}
	p1 := parts[0].partInfo()
	if e.arc == nil {
		return e.readList(parts, p1, missing)
	}
	if p1.into == "" {
		return false, nil // part 1's card asks where to extract
	}

	mayFree, err := e.mayFree(parts, p1, missing)
	if err != nil {
		return false, err
	}
	if e.x == nil {
		out := p1.out
		if out == "" {
			if out, err = e.nameOut(parts[0], p1.into); err != nil {
				return false, err
			}
		} else if !exists(out) {
			return false, errFolderRemoved
		}
		need := spaceNeeded(e.arc, p1.done)
		if !mayFree {
			err = e.roomToKeep(p1, missing, need)
			if err != nil && p1.auto {
				// Automatic kept it, but there is no longer room (a restart):
				// it frees it instead of pausing, as mayFree would.
				log.Printf("%s: no room to keep the archive any more, so it is freed as it is extracted", e.who)
				e.setKeeping(parts, KeepFree)
				mayFree, err = true, e.freeDone(parts, p1.done)
			}
		}
		if err == nil && mayFree {
			err = checkSpace(p1.into, need)
		}
		if err != nil {
			return false, err
		}
		x, err := archive.NewExtractorFrom(e.arc, e.file, out, p1.done)
		if errors.Is(err, archive.ErrWrongPassword) {
			return e.askPassword(parts[0], true)
		}
		if err != nil {
			return false, err
		}
		x.Mark = e.mark
		e.x, e.out = x, out
		files, of := x.Progress()
		parts[0].editFolder(func(f *Folder) bool {
			f.Files, f.Of = files, of
			return true
		})
	}
	if !exists(e.out) {
		return false, errFolderRemoved
	}

	e.x.Keep = !mayFree
	e.x.Freeing = func(rs []archive.Range) error { return e.saveFreed(parts, rs) }
	res, xerr := e.x.Extract(ctx, missing)
	e.record(parts, res)
	e.streaming.Store(e.x.Streaming())
	e.passed = e.x.Passed()
	if errors.Is(xerr, archive.ErrWrongPassword) {
		return e.askPassword(parts[0], true)
	}
	if xerr != nil && !exists(e.out) {
		xerr = errFolderRemoved
	}
	return res.More, xerr
}

// mayFree is the one place that says whether the archive's bytes may be
// freed as they are extracted now, by part 1's archive keeping (ADR 0007).
// Automatic is decided the first time it is asked, by whether there is room
// to keep the archive, and recorded on the parts so a restart keeps to it.
// While it keeps the archive and either drive falls below diskReserve, it
// frees it instead, rather than the download pausing (keptByAutoLocked).
func (e *Extraction) mayFree(parts []archivePart, p1 partInfo, missing []archive.Range) (bool, error) {
	switch p1.keep {
	case KeepFree:
		return true, nil
	case "":
		k := KeepFree
		if e.roomToKeep(p1, missing, spaceNeeded(e.arc, p1.done)) == nil {
			k = KeepUntil
		}
		log.Printf("%s: automatic archive keeping: %s", e.who, k)
		e.setKeeping(parts, k)
		return k == KeepFree, nil
	}
	if !p1.auto || time.Since(e.roomAt) < 5*time.Second {
		return false, nil
	}
	e.roomAt = time.Now()
	if freeSpaceBelow(filepath.Dir(p1.path), diskReserve) || freeSpaceBelow(p1.into, diskReserve) {
		log.Printf("%s: the disk is almost full, so the archive is freed as it is extracted after all", e.who)
		e.setKeeping(parts, KeepFree)
		return true, e.freeDone(parts, p1.done)
	}
	return false, nil
}

// setKeeping records what Automatic decided on every part there is.
func (e *Extraction) setKeeping(parts []archivePart, k string) {
	for _, p := range parts {
		if p != nil {
			p.editFolder(func(f *Folder) bool {
				f.Keeping = k
				return true
			})
		}
	}
}

// freeDone frees the bytes of the Entries extracted while the archive was
// kept, now that it is freed after all: they are never looked at again.
func (e *Extraction) freeDone(parts []archivePart, done []int64) error {
	if e.arc == nil || len(done) == 0 {
		return nil
	}
	var rs []archive.Range
	for _, en := range e.arc.Entries {
		if slices.Contains(done, en.Start) {
			rs = append(rs, archive.Range{Start: en.Start, End: en.End})
		}
	}
	if err := e.saveFreed(parts, rs); err != nil {
		return err
	}
	for _, r := range rs {
		if err := e.file.Free(r.Start, r.End-r.Start); err != nil {
			return err
		}
	}
	return nil
}

// roomToKeep is checkSpace for keeping the archive until it is extracted:
// the .part's drive holds the archive's bytes still to download, and the
// output drive need, what it extracts to; added up when they are one drive.
// shortcut: a Set is taken to be on part 1's drive, and a RAR's volumes not
// laid out yet aren't counted; look at each part's drive if sets get split.
func (e *Extraction) roomToKeep(p1 partInfo, missing []archive.Range, need int64) error {
	total, _ := e.parts.Size()
	var fetch int64
	for _, r := range missing {
		fetch += max(0, min(r.End, total)-r.Start)
	}
	dir := filepath.Dir(p1.path)
	if strings.EqualFold(filepath.VolumeName(dir), filepath.VolumeName(p1.into)) {
		return checkSpace(p1.into, need+fetch)
	}
	if err := checkSpace(dir, fetch); err != nil {
		return err
	}
	return checkSpace(p1.into, need)
}

// readList reads the archive's list of files once its bytes are on disk: a
// ZIP's or 7z's tail, fetched first, and its front; a RAR in volumes' front.
func (e *Extraction) readList(parts []archivePart, p1 partInfo, missing []archive.Range) (bool, error) {
	total, _ := e.parts.Size()
	n := e.parts.Len()
	if !e.volumes {
		last := e.parts.Span(n - 1)
		if e.need == 0 {
			// The list of files is at the end: guess within the last part,
			// so the one before it needn't be there; Need widens it if it must.
			e.need = max(min(archive.TailGuess, last.End-last.Start), 1)
		}
		from := total - e.need
		if !onDiskA(missing, from, total) {
			// The part holding the tail fetches it first, and starts first for it.
			if p := parts[n-1]; p != nil {
				p.preferTail(max(from-last.Start, 0))
			}
			e.tail = n - 1
			return false, nil
		}
		if p := parts[n-1]; p != nil {
			p.preferTail(-1) // the tail is there: front to back again
		}
	}
	if e.refetchList(parts, total) {
		return false, nil
	}
	if !onDiskA(missing, 0, min(e.head, e.parts.Span(0).End)) {
		return false, nil // the front comes with the download, which fetches it anyway
	}
	if p1.ask != "" {
		e.asking = true
		return false, nil
	}
	var a *archive.Archive
	var err error
	if e.volumes {
		a, err = archive.ReadVolumes(e.parts, p1.password)
	} else {
		var need int64
		need, err = archive.NeedWith(e.file, total, e.need, p1.password)
		if err == nil && need > e.need {
			e.need, e.tail = need, n-1 // more of its end, then
			return true, nil
		}
		if err == nil {
			a, err = archive.ReadWith(e.file, total, p1.password)
		}
	}
	switch {
	case errors.Is(err, archive.ErrWrongPassword):
		return e.askPassword(parts[0], true)
	case errors.Is(err, archive.ErrPassword):
		return e.askPassword(parts[0], false)
	case err != nil:
		return false, err
	}
	e.arc = a
	e.reclaim(parts, p1.done)
	var renamed []string
	for _, r := range a.Renamed {
		renamed = append(renamed, r.From+" → "+r.To)
	}
	parts[0].editFolder(func(f *Folder) bool {
		f.Renamed = renamed
		return true
	})
	return true, nil // what is on disk changed: look again
}

// refetchList has a RAR's freed bytes fetched again before its list of files
// is read, where a run stopped partway (an engine restart) freed them: a RAR
// is one Entry read from its front, with headers in every volume, so read as
// they are its zeroed bytes make it look like no RAR at all. A ZIP's or 7z's
// list is at its end, which is never freed, and reclaim fetches the rest of
// their Entries not done once it is read.
func (e *Extraction) refetchList(parts []archivePart, total int64) bool {
	if !e.volumes && !strings.EqualFold(filepath.Ext(e.name), ".rar") {
		return false
	}
	if !e.refetch(parts, archive.Range{Start: 0, End: total}, true) {
		return false
	}
	log.Printf("%s: its list of files was freed by a run stopped partway, fetching it again", e.who)
	return true
}

// askPassword has the extraction wait for the archive's password, the first
// one or after a wrong one. Part 1 holds it, as it holds the switch; the
// downloads go on, as nothing is freed until the archive opens.
func (e *Extraction) askPassword(p1 archivePart, wrong bool) (bool, error) {
	e.close()
	e.arc, e.asking = nil, true
	ask := AskPassword
	if wrong {
		ask = AskWrong
	}
	p1.editFolder(func(f *Folder) bool {
		f.Password, f.Ask = "", ask
		return true
	})
	return false, nil
}

// close lets go of the decompressor and its files in the output folder.
func (e *Extraction) close() {
	if e.x != nil {
		e.x.Close()
		e.x = nil
	}
	e.streaming.Store(0)
}

// nameOut picks and makes the output folder in into: the archive's Root, or
// else the archive's name without its extension, numbered if taken.
func (e *Extraction) nameOut(p1 archivePart, into string) (string, error) {
	base := e.arc.Root
	if base == "" {
		base = strings.TrimSuffix(e.name, filepath.Ext(e.name))
	}
	if strings.TrimSpace(base) == "" {
		base = "download" // an archive named only ".zip"
	}
	if err := os.MkdirAll(into, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(into, uniqueName(into, base, ""))
	if err := os.Mkdir(out, 0o755); err != nil {
		return "", err
	}
	p1.editFolder(func(f *Folder) bool {
		f.Out = out
		return true
	})
	return out, nil
}

// reclaim has the bytes freed of Entries not done fetched again: one stopped
// partway (a pause, a crash, a failed run) is extracted again from its
// front, so the bytes freed behind its decompressor are needed again.
func (e *Extraction) reclaim(parts []archivePart, done []int64) {
	was := map[int64]bool{}
	for _, s := range done {
		was[s] = true
	}
	for _, en := range e.arc.Entries {
		if !was[en.Start] && e.refetch(parts, archive.Range{Start: en.Start, End: en.End}, true) {
			log.Printf("%s: %s was stopped partway, fetching its freed bytes again", e.who, en.Name)
		}
	}
}

// refetch has the parts fetch r again: only what is freed of it, or all of
// it. It reports whether any part has anything to fetch.
func (e *Extraction) refetch(parts []archivePart, r archive.Range, freedOnly bool) bool {
	idx, local := e.parts.Cut(r)
	any := false
	for k, i := range idx {
		p := parts[i]
		if p == nil {
			continue
		}
		want := []Range{{local[k].Start, local[k].End}}
		if freedOnly {
			want = intersectRanges(want, p.partInfo().freed)
		}
		for _, w := range want {
			p.refetch(w)
			any = true
		}
	}
	return any
}

// record saves what Extract did: the Entries done, the count of files and
// those that came out wrong on part 1, so a restart doesn't forget them, and
// the bytes freed on the part they are in. A wrong Entry is fetched again;
// wrong a second time, the parts holding it pause.
func (e *Extraction) record(parts []archivePart, res archive.Result) {
	files, of := e.x.Progress()
	e.finished = e.x.Finished()
	var twice []*archive.Failure
	parts[0].editFolder(func(f *Folder) bool {
		changed := len(res.Done) > 0 || len(res.Failed) > 0 || f.Files != files || f.Of != of
		for _, en := range res.Done {
			f.Done = append(f.Done, en.Start)
		}
		f.Files, f.Of = files, of
		for _, fl := range res.Failed {
			if i := slices.Index(f.Failed, fl.Range.Start); i >= 0 {
				// Its next try after a resume gets another fetch first.
				f.Failed = slices.Delete(f.Failed, i, i+1)
				twice = append(twice, fl)
			} else {
				f.Failed = append(f.Failed, fl.Range.Start)
			}
		}
		if e.finished && f.Password != "" {
			f.Password, changed = "", true // kept only as long as it's needed
		}
		return changed
	})

	// res.Freed is on the parts already: saveFreed recorded it before it was freed.
	for _, fl := range res.Failed {
		log.Printf("%s: %s, fetching it again", e.who, fl)
		e.refetch(parts, fl.Range, false)
	}
	for _, fl := range twice {
		msg := fmt.Sprintf("Paused: %s came out wrong twice (%s). Resume to download it again", fl.Entry.Name, fl.Reason)
		idx, _ := e.parts.Cut(fl.Range)
		for _, i := range idx {
			if p := parts[i]; p != nil {
				p.pause(msg)
			}
		}
	}
	e.freed = len(res.Freed) > 0
	e.idle = len(res.Done) == 0 && len(res.Freed) == 0 && len(res.Failed) == 0 && !res.More
}

// partInfo is what an Extraction and a Set read of a part, under its lock.
type partInfo struct {
	d         *Download
	on        bool
	into      string
	size      int64
	probed    bool
	completed bool
	path      string
	missing   []Range // in the part
	freed     []Range
	done      []int64
	out       string
	allocated bool
	note      string
	name      string
	password  string // part 1's: the archive's
	keep      string // archive keeping in force (Folder.keepNow), "" for Automatic not decided yet
	auto      bool   // archive keeping is Automatic
	ask       string
	files, of int
}

func (d *Download) partInfo() partInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := partInfo{d: d, size: d.st.Size, probed: d.st.Probed, completed: d.st.Status == StatusCompleted,
		path: d.partPath(), allocated: d.st.Allocated, name: d.st.Filename}
	if f := d.st.Folder; f != nil {
		p.on, p.into, p.freed, p.done, p.out, p.note = f.On, f.Into, slices.Clone(f.Freed), slices.Clone(f.Done), f.Out, f.Note
		p.password, p.ask, p.files, p.of = string(f.Password), f.Ask, f.Files, f.Of
		def := d.keepDefault()
		p.keep, p.auto = f.keepNow(def), f.keepChoice(def) == KeepAuto
	}
	switch {
	case p.completed:
	case d.plan != nil:
		p.missing = d.plan.ranges()
	case d.st.Allocated:
		p.missing = slices.Clone(d.st.Remaining)
	default:
		p.missing = []Range{{0, d.st.Size}}
	}
	return p
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

// editFolder is archivePart's: it changes the download's Folder under its lock.
func (d *Download) editFolder(edit func(f *Folder) bool) {
	d.mu.Lock()
	if d.st.Folder == nil {
		d.st.Folder = &Folder{}
	}
	changed := edit(d.st.Folder)
	d.mu.Unlock()
	if changed {
		d.env.Changed(d)
	}
}

// save is archivePart's: it writes the download's state to disk at once.
func (d *Download) save() error { return d.env.Save(d) }

// pause is archivePart's: it stops the download with msg as why. A
// download's own extraction ends its step with the error instead, as its run
// waits for that extraction to stop, and so can't be waited for from it.
func (d *Download) pause(msg string) {
	d.mu.Lock()
	fr := d.fold
	if fr != nil {
		fr.paused = &pauseError{msg: msg}
	}
	d.mu.Unlock()
	if fr == nil {
		d.pauseWith(msg)
	}
}

// pauseWith pauses the part and says why; the Set's loop holds no part's lock.
func (d *Download) pauseWith(msg string) {
	d.Stop(StatusPaused)
	d.mu.Lock()
	if d.st.Status == StatusPaused {
		d.st.Error = msg
	}
	d.mu.Unlock()
	d.env.Changed(d)
}

// preferTail is archivePart's: a running download fetches from at on first,
// or from nowhere first again for -1.
func (d *Download) preferTail(at int64) {
	d.mu.Lock()
	if d.plan != nil && d.plan.prefer != at {
		if at >= 0 {
			d.plan.cut(at)
		}
		d.plan.prefer = at
	}
	d.mu.Unlock()
}

// refetch is archivePart's: r is put back to be fetched, and what was freed of
// it no longer counts as freed. A completed part is downloaded again.
func (d *Download) refetch(r Range) {
	d.mu.Lock()
	if f := d.st.Folder; f != nil {
		f.Freed = subtractRanges(f.Freed, []Range{r})
	}
	switch {
	case d.plan != nil:
		for _, p := range subtractRanges([]Range{r}, d.plan.ranges()) {
			d.plan.add(p)
		}
	default:
		d.st.Remaining = mergeRanges(append(d.st.Remaining, r))
		if d.st.Status == StatusCompleted {
			d.st.Status, d.st.Finished = StatusQueued, time.Time{}
		}
	}
	d.mu.Unlock()
	log.Printf("download %s: fetching %d-%d again", d.ID(), r.Start, r.End)
	d.env.Changed(d)
}
