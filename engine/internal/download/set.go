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
	"sync"
	"sync/atomic"
	"time"

	"downloadmanagersquared/engine/internal/archive"
)

// A Set extracts a split archive (x.7z.001, x.7z.002, …, a RAR in volumes:
// x.part1.rar, x.part2.rar, … or x.rar, x.r00, …, or a split ZIP: x.z01,
// x.z02, …, x.zip, or x.zip.001, …) whose parts are downloads of their own,
// as one archive into one output folder.
//
// Each part downloads as usual and completes once its bytes are fetched; with
// Download as folder on, its .part stays for the Set instead of being
// renamed. The Set reads the parts end to end through its own handles, frees
// each part's bytes once they are extracted (recorded in that part's
// Folder.Freed), and deletes a completed part's .part once it is all freed.
// Part 1 holds the Set's own record (Folder.Done, Files, Of, Out) and its
// switch: the other parts follow it. A missing part stops extraction at the
// gap; the Summary says which.
//
// A 7z's parts are laid out from part 1's start header. A RAR's volumes are
// laid out as their downloads are known, in order, and its decompressor
// waits at the end of the last one laid out for the next. A split ZIP's
// .zip says how many parts there are; a cut one (.zip.001…) is laid out as
// its parts are known, until its last part's tail reads as a ZIP's end.
type Set struct {
	Key string
	set string // the archive's name, x.7z, x.rar or x.zip
	rar bool
	zip bool

	mu      sync.Mutex
	members map[int]*Download // by part number, from 1
	trash   []string          // .parts of parts removed with their files, for the loop to delete
	stopped bool

	view     atomic.Pointer[SetSummary]
	tail     atomic.Pointer[Download] // the part whose end the Set needs before it can extract anything: a 7z's or ZIP's list of files is at the end of its last part; it starts first, fetches its end first, and gives way again once nil
	inOrder  atomic.Bool              // read front to back only (a RAR, a solid 7z, one huge Entry): a part fetched before the ones ahead of it waits whole on disk; else fetching several at once costs no space
	gaveUp   string                   // the reason last given up for (the loop's alone)
	accepted atomic.Bool              // the archive's list of files has been read: the Set extracts as parts come, and completed parts keep their .part; until then only part 1 and tail are of any use
	finished atomic.Bool              // all extracted: no part's .part is needed
	cost     atomic.Int64             // RestartCost, as of the last step

	cancel context.CancelFunc
	done   chan struct{}

	// A move of a part's folder needs the run's handles closed (Hold).
	holds  atomic.Int32
	stepMu sync.Mutex // held while the loop steps; guards run
	run    *setRun    // the loop\'s
	curMu  sync.Mutex
	cur    context.CancelFunc                           // the step under way
	stepFn func(context.Context, *setRun) (bool, error) // tests: stands in for step
}

// Start extracts in the background until Stop.
func (s *Set) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	go s.loop(ctx)
}

// Holds is whether d is one of the Set's parts.
func (s *Set) Holds(d *Download) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.members {
		if m == d {
			return true
		}
	}
	return false
}

// Stop stops extracting and waits until it has.
func (s *Set) Stop() {
	if s.cancel != nil {
		s.cancel()
		<-s.done
	}
}

// setRun is a Set's extraction while it runs: rebuilt from the parts' record
// after a restart.
type setRun struct {
	failing  bool // the last step failed (not a pause): the parts are fetched, the extraction is not
	parts    *archive.Parts
	files    []*os.File  // the Set's own handles, by part index
	released []bool      // the part's .part is deleted: all of it is freed
	ex       *Extraction // once the parts are laid out
	out      string
	files1   int // files extracted and in the archive, kept once the Extractor is closed
	of       int
	finished bool
	note     string
	disks    int       // a split ZIP's parts, once its .zip says
	tail     *Download // the part whose end this step waits for

	extracted, sizes int64 // bytes extracted of the parts (freed or kept), and the parts' sizes
	locked           bool  // some part's bytes are freed
	phase            string
	pace             meter
}

func (r *setRun) close() {
	if r.ex != nil {
		r.ex.close()
	}
	for i, f := range r.files {
		if f != nil {
			f.Close()
			r.files[i] = nil
			r.parts.Set(i, nil)
		}
	}
}

func (s *Set) loop(ctx context.Context) {
	run := &setRun{}
	s.run = run
	defer func() {
		s.stepMu.Lock()
		defer s.stepMu.Unlock()
		if run.parts != nil {
			run.close()
		}
		s.empty(run, true)
		close(s.done)
	}()
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	logged := "" // the error last logged: one that stays is tried again, so not every time
	fails := 0   // steps failed in a row: the wait before the next grows (renameDelay)
	for {
		more, err := s.runStep(ctx, run)
		if err == nil {
			logged, fails = "", 0
		}
		if err != nil && ctx.Err() == nil {
			fails++
			if err.Error() != logged {
				logged = err.Error()
				log.Printf("set %s: %v", s.set, err)
			}
			run.note = UserText(err)
			failing := !errors.As(err, new(*pauseError))
			if failing {
				// The parts are downloaded: it is the extraction that failed, and it tries again by itself.
				run.note = "Extraction failed, trying again: " + run.note
			}
			if run.parts != nil {
				run.close()
			}
			s.stepMu.Lock()
			*run = setRun{note: run.note, failing: failing}
			s.stepMu.Unlock()
		}
		s.stepMu.Lock()
		s.publish(run)
		s.stepMu.Unlock()
		if !more {
			wait := t.C
			if fails > 0 {
				// A step that fails again rebuilds the run and fetches the freed bytes again: not five times a second.
				tm := time.NewTimer(renameDelay(fails))
				defer tm.Stop()
				wait = tm.C
			}
			select {
			case <-ctx.Done():
				return
			case <-wait:
			}
		}
	}
}

// runStep is one step of the loop, not while a move has the run's handles
// closed (Hold).
func (s *Set) runStep(ctx context.Context, run *setRun) (more bool, err error) {
	s.stepMu.Lock()
	defer s.stepMu.Unlock()
	if s.holds.Load() > 0 {
		s.dropRun(run)
		return false, nil
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.curMu.Lock()
	s.cur = cancel
	s.curMu.Unlock()
	if s.holds.Load() > 0 {
		cancel()
	}
	step := s.step
	if s.stepFn != nil {
		step = func(ctx context.Context, run *setRun) (bool, error) { return s.stepFn(ctx, run) }
	}
	more, err = step(sctx, run)
	if s.holds.Load() > 0 {
		s.dropRun(run)
		return false, nil
	}
	return more, err
}

func (s *Set) dropRun(run *setRun) {
	if run.parts != nil {
		run.close()
	}
	*run = setRun{}
}

// Hold stops the Set's step and has it let go of every file until the
// returned function is called: the parts' .part files and the output folder
// can't be moved while it has them open. The run is built again from the
// parts' record after.
func (s *Set) Hold() (release func()) {
	if s.done == nil {
		return func() {}
	}
	s.holds.Add(1)
	s.curMu.Lock()
	if s.cur != nil {
		s.cur()
	}
	s.curMu.Unlock()
	s.stepMu.Lock() // the step under way has ended
	s.dropRun(s.run)
	s.stepMu.Unlock()
	return func() { s.holds.Add(-1) }
}

// step does what can be done now. more: call again at once.
func (s *Set) step(ctx context.Context, run *setRun) (more bool, err error) {
	s.empty(run, false)
	s.mu.Lock()
	members := make(map[int]partInfo, len(s.members))
	for n, d := range s.members {
		members[n] = d.partInfo()
	}
	s.mu.Unlock()
	run.note, run.tail, run.phase, run.failing = "", nil, "", false
	if m, ok := members[archive.LastPart]; ok && run.disks > 0 {
		delete(members, archive.LastPart) // a split ZIP's .zip: its last part
		members[run.disks] = m
	}
	run.extracted, run.sizes, run.locked = 0, 0, false
	for _, m := range members {
		run.extracted += sumRanges(m.freed)
		run.locked = run.locked || len(m.freed) > 0
		switch {
		case m.probed && m.size > 0:
			run.sizes += m.size
		case members[1].probed:
			run.sizes += members[1].size // not started yet: about as long as part 1
		}
	}

	p1, ok := members[1]
	if ok && !p1.on && !p1.completed && strings.HasPrefix(p1.note, archive.ErrSplit.Error()) {
		// A plain x.rar read as one archive before its x.r00 was there: it is the set's part 1.
		p1.d.mu.Lock()
		if f := p1.d.st.Folder; f != nil && !f.locked() {
			f.On, f.Note = true, ""
			p1.on = true
		}
		p1.d.mu.Unlock()
		p1.d.changed()
	}
	s.alreadyExtracted(run, members)
	switch {
	case run.finished:
		s.sweep(run.out, members) // a .part left by a failed save or a stop in the middle of finish
		return false, nil
	case !ok:
		run.note = "Part 1 missing"
		return false, nil
	case !p1.on:
		// Switched off: the parts are let go, and one already fetched for the
		// Set gets its own name back.
		if run.parts != nil {
			run.close()
			*run = setRun{}
		}
		for _, m := range members {
			if m.completed && !m.on && len(m.freed) == 0 && exists(m.path) {
				m.d.keepPart(m.path)
				m.d.changed()
			}
		}
		return false, nil
	case !p1.probed:
		return false, nil
	}
	s.follow(p1, members)

	cut := s.zip && strings.HasPrefix(strings.ToLower(p1.name), strings.ToLower(s.set)+".")
	switch {
	case s.rar, cut && run.arc() == nil:
		s.layVolumes(run, members, cut)
		if run.tail != nil {
			return false, nil // a cut file's last piece is to say its size first
		}
	case s.zip && run.parts == nil:
		if err := s.layZip(run, members); err != nil {
			return false, s.giveUp(run, members, err)
		}
	}
	if (s.rar || s.zip) && run.parts == nil {
		return false, nil
	}
	// The layout: from part 1's start header and size.
	if run.parts == nil {
		if !p1.allocated || !onDisk(p1.missing, 0, 32) {
			return false, nil
		}
		f, err := os.Open(p1.path)
		if err != nil {
			return false, nil // not there yet
		}
		front := make([]byte, 32)
		_, err = f.ReadAt(front, 0)
		f.Close()
		if err != nil {
			return false, err
		}
		sizes, err := archive.SplitSizes(front, p1.size)
		if err != nil {
			return false, s.giveUp(run, members, err)
		}
		run.parts = archive.NewParts(sizes)
		run.files = make([]*os.File, len(sizes))
		run.released = make([]bool, len(sizes))
	}
	if run.ex == nil {
		// A RAR's headers are at its front; a 7z's start header and a ZIP's
		// first bytes say what it is, and its list of files is at its end.
		head := int64(32)
		if s.rar {
			head = archive.HeadLen
		}
		run.ex = &Extraction{name: s.set, who: "set " + s.set, file: run.parts, parts: run.parts,
			head: head, volumes: s.rar, mark: p1.d.markFunc(), tail: -1}
	}
	n := run.parts.Len()
	for k, m := range members {
		if s.rar || cut {
			break // laid out as they come
		}
		if k > n {
			return false, s.giveUp(run, members, &archive.Unsupported{Reason: fmt.Sprintf("Part %d is more than the archive has (%d)", k, n)})
		}
		if want := run.parts.Span(k - 1); m.probed && m.size != want.End-want.Start {
			return false, s.giveUp(run, members, &archive.Unsupported{Reason: fmt.Sprintf("Part %d is %s, not the %s the archive needs", k, human(m.size), human(want.End-want.Start))})
		}
	}

	run.openParts(members)

	missing, gone := s.missingRanges(run, members)
	if len(gone) > 0 {
		run.note = partsMissing(gone)
	}

	ps := make([]archivePart, n)
	for i := range n {
		if m, ok := members[i+1]; ok {
			ps[i] = m.d
		}
	}
	more, err = run.ex.step(ctx, ps, missing)
	run.extracted = max(run.extracted, run.ex.passed) // a kept archive frees nothing
	if run.ex.tail >= 0 {
		if last, ok := members[run.ex.tail+1]; ok {
			run.tail = last.d // its end first, and it starts first for it (Tail)
		}
	}
	if run.ex.asking {
		run.note = passwordNote
	}
	run.out = run.ex.out
	var u *archive.Unsupported
	switch {
	case err == nil:
	case errors.As(err, &u) && run.ex.arc == nil && cut && wholePiece(run.parts):
		// A cut ZIP whose last part known is a whole piece: more may follow.
		if _, ok := members[n+1]; !ok {
			run.note = partsMissing([]int{n + 1})
		}
		return false, nil
	case errors.As(err, &u):
		return false, s.giveUp(run, members, err)
	case run.ex.arc == nil:
		return false, nil // its list of files isn't all there yet
	default:
		return false, err
	}
	if run.ex.arc != nil {
		s.accepted.Store(true)
	}
	if run.ex.finished {
		run.finished = true
		s.finish(run, members)
		return false, nil
	}
	if run.ex.x == nil {
		return more, nil // it waits for the folder or the password
	}
	s.release(run, members)
	run.phase = "Extracting"
	if run.ex.idle {
		// Nothing to do with what is on disk: it waits for the first part still to be fetched.
		for i := range run.parts.Len() {
			if m, ok := members[i+1]; ok && !run.released[i] && !m.completed {
				run.phase = fmt.Sprintf("Waiting for part %d", i+1)
				break
			}
		}
	}
	return more, nil
}

// alreadyExtracted: the Set was extracted before the engine last stopped, and
// the parts are gone.
func (s *Set) alreadyExtracted(run *setRun, members map[int]partInfo) {
	p1, ok := members[1]
	if !run.finished {
		// Extracted before the engine last stopped: every part was given the
		// output folder then (until then only part 1 has it), and part 1 has
		// counted all the files. Nothing is left to read: the parts are gone.
		out := ""
		if ok && p1.completed && p1.out != "" && p1.of > 0 && p1.files == p1.of {
			out = p1.out
		}
		for n, m := range members {
			if n != 1 && m.completed && m.out != "" {
				out = m.out
			}
		}
		if out != "" {
			run.finished, run.out, run.files1, run.of = true, out, p1.files, p1.of
			s.finished.Store(true)
		}
	}
}

// openParts takes the Set's own handles on the parts that have a .part.
func (run *setRun) openParts(members map[int]partInfo) {
	n := run.parts.Len()
	for i := range n {
		m, ok := members[i+1]
		if run.files[i] != nil || run.released[i] || !ok || !m.allocated {
			continue
		}
		f, err := os.OpenFile(m.path, os.O_RDWR, 0)
		if err != nil {
			continue
		}
		run.files[i] = f
		run.parts.Set(i, archive.Sparse(f))
	}
}

// missingRanges is what isn't on disk, in the whole archive, and the parts
// not added (by number).
func (s *Set) missingRanges(run *setRun, members map[int]partInfo) (missing []archive.Range, gone []int) {
	n := run.parts.Len()
	for i := range n {
		span := run.parts.Span(i)
		m, ok := members[i+1]
		switch {
		case run.released[i]:
		case !ok:
			gone = append(gone, i+1)
			missing = append(missing, archive.Range{Start: span.Start, End: span.End})
		case run.files[i] == nil:
			missing = append(missing, archive.Range{Start: span.Start, End: span.End})
		default:
			for _, r := range m.missing {
				missing = append(missing, archive.Range{Start: span.Start + r.Start, End: span.Start + r.End})
			}
		}
	}
	total := run.total()
	if s.rar {
		// The volumes not laid out yet: the decompressor waits for them.
		missing = append(missing, archive.Range{Start: total, End: archive.OpenEnd})
		if w := run.parts.Waiting(); w > n && members[w].d == nil {
			gone = append(gone, w)
		}
	}
	return missing, gone
}

const passwordNote = "Needs the archive's password"

// arc is the archive's list of files, nil until it is read.
func (r *setRun) arc() *archive.Archive {
	if r.ex == nil {
		return nil
	}
	return r.ex.arc
}

// layVolumes lays out a RAR's volumes known so far: each after the last, once
// its download knows its size, with none missing before it. A cut file's
// pieces are all one size but the last: one not started yet is taken to be
// as long as the first, and the last one known starts first, to say its own
// size and bring the list of files at its end.
func (s *Set) layVolumes(run *setRun, members map[int]partInfo, cut bool) {
	k := 0
	if run.parts != nil {
		k = run.parts.Len()
	}
	hi := 0
	for n := range members {
		hi = max(hi, n)
	}
	var add []int64
	for n := k + 1; ; n++ {
		m, ok := members[n]
		size := m.size
		if ok && !m.probed && cut && n < hi && members[1].probed {
			size = members[1].size
		} else if !ok || !m.probed || size <= 0 {
			if ok && cut && n == hi {
				run.tail = m.d
			}
			break
		}
		add = append(add, size)
	}
	if len(add) == 0 {
		return
	}
	if run.ex != nil {
		run.ex.need = 0 // the last part changed
	}
	if run.parts == nil {
		run.parts = archive.NewParts(add)
	} else {
		run.parts.Grow(add...)
	}
	run.files = append(run.files, make([]*os.File, len(add))...)
	run.released = append(run.released, make([]bool, len(add))...)
}

// wholePiece: a cut file's last part laid out is as long as its first, so more may follow.
func wholePiece(p *archive.Parts) bool {
	first, last := p.Span(0), p.Span(p.Len()-1)
	return p.Len() > 0 && last.End-last.Start == first.End-first.Start
}

// layZip lays out a split ZIP (x.z01…, x.zip) once its .zip's tail says how
// many parts there are and each of them knows its size.
func (s *Set) layZip(run *setRun, members map[int]partInfo) error {
	if run.disks == 0 {
		last, ok := members[archive.LastPart]
		if !ok {
			run.note = "Its .zip part missing"
			return nil
		}
		from := max(last.size-archive.TailGuess, 0)
		if !last.probed || !last.allocated || !onDisk(last.missing, from, last.size) {
			if last.probed {
				last.d.preferTail(from)
			}
			run.tail = last.d
			return nil
		}
		f, err := os.Open(last.path)
		if err != nil {
			return nil // not there yet
		}
		buf := make([]byte, last.size-from)
		_, err = f.ReadAt(buf, from)
		f.Close()
		if err != nil {
			return err
		}
		n, err := archive.ZipParts(buf)
		if err != nil {
			return err
		}
		run.disks = n
		delete(members, archive.LastPart)
		members[n] = last
	}
	var sizes []int64
	var gone []int
	for i := 1; i <= run.disks; i++ {
		m, ok := members[i]
		switch {
		case !ok:
			gone = append(gone, i)
		case m.probed && m.size > 0:
			sizes = append(sizes, m.size)
		case i < run.disks && members[1].probed && members[1].size > 0:
			// Not started yet: a split ZIP's parts are all one size but the
			// last. One that turns out another size stops the extraction.
			sizes = append(sizes, members[1].size)
		}
	}
	if len(gone) > 0 {
		run.note = partsMissing(gone)
	}
	if len(sizes) < run.disks {
		return nil
	}
	run.parts = archive.NewParts(sizes)
	run.files = make([]*os.File, run.disks)
	run.released = make([]bool, run.disks)
	return nil
}

func (r *setRun) total() int64 {
	t, _ := r.parts.Size()
	return t
}

func partsMissing(gone []int) string {
	if len(gone) == 1 {
		return fmt.Sprintf("Part %d missing", gone[0])
	}
	s := make([]string, len(gone))
	for i, g := range gone {
		s[i] = fmt.Sprint(g)
	}
	return "Parts " + strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1] + " missing"
}

// follow has every part take part 1's switch and folder: the Set is one
// download as folder.
func (s *Set) follow(p1 partInfo, members map[int]partInfo) {
	keep, keeping := p1.d.folderKeep()
	for n, m := range members {
		if n == 1 || m.on && m.into == p1.into && !m.d.keepDiffers(keep, keeping) {
			continue
		}
		d := m.d
		d.mu.Lock()
		if d.st.Folder == nil {
			d.st.Folder = &Folder{}
		}
		if d.st.Folder.locked() {
			d.mu.Unlock()
			continue // its bytes are freed: its choice can't change any more
		}
		// The Note goes too: one read alone before its set was known says it is a part of one.
		d.st.Folder.On, d.st.Folder.Auto, d.st.Folder.Into, d.st.Folder.Note = true, false, p1.into, ""
		d.st.Folder.Keep, d.st.Folder.Keeping = keep, keeping
		if !d.st.Allocated {
			d.st.Dir = p1.into
		}
		d.mu.Unlock()
		d.changed()
	}
}

// keepDiffers: the download's archive keeping, as chosen and as Automatic
// decided, is not keep and keeping.
func (d *Download) keepDiffers(keep, keeping string) bool {
	k, kg := d.folderKeep()
	return k != keep || kg != keeping
}

// folderKeep is the download's archive keeping as chosen and as Automatic
// decided, for the other parts of its Set to take.
func (d *Download) folderKeep() (keep, keeping string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if f := d.st.Folder; f != nil {
		return f.Keep, f.Keeping
	}
	return "", ""
}

// release deletes the .part of every completed part that is all freed, once
// the parts' record says so on disk: part 1's Entries done and the part's
// bytes freed. A crash after the delete would otherwise find its bytes neither
// on disk nor saved as freed.
func (s *Set) release(run *setRun, members map[int]partInfo) {
	if run.ex.x.Keep {
		return // the archive is kept (Extraction.mayFree): a part with little in it isn't let go either
	}
	saved1 := false
	for i := range run.parts.Len() {
		m, ok := members[i+1]
		if !ok || run.released[i] || run.files[i] == nil || !m.completed {
			continue
		}
		m.d.mu.Lock()
		freed := slices.Clone(m.d.st.Folder.Freed)
		m.d.mu.Unlock()
		if sumRanges(subtractRanges([]Range{{0, m.size}}, freed)) > 64<<10 {
			continue // bytes the list of files or a block still needs
		}
		if !run.arcCovers(i) {
			continue
		}
		if !saved1 {
			if err := members[1].d.save(); err != nil {
				log.Printf("set %s: saving part 1: %v", s.set, err)
				return
			}
			saved1 = true
		}
		if i > 0 {
			if err := m.d.save(); err != nil {
				log.Printf("set %s: saving part %d: %v", s.set, i+1, err)
				continue
			}
		}
		run.files[i].Close()
		run.files[i] = nil
		run.parts.Set(i, nil)
		run.released[i] = true
		if err := os.Remove(m.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("set %s: removing part %d: %v", s.set, i+1, err)
		}
	}
}

// arcCovers: every block with bytes in part i is done.
func (r *setRun) arcCovers(i int) bool {
	span := r.parts.Span(i)
	for k, e := range r.ex.arc.Entries {
		if e.Start < span.End && span.Start < e.End && !r.ex.x.Done(k) {
			return false
		}
	}
	// The list of files sits at the end of the last part: once read, it isn't needed again.
	return true
}

// finish ends the Set: all is extracted, and no part's .part is needed.
func (s *Set) finish(run *setRun, members map[int]partInfo) {
	out := run.out
	run.files1, run.of = run.ex.x.Progress()
	run.close()
	s.finished.Store(true)
	for _, m := range members {
		d := m.d
		d.mu.Lock()
		d.st.Folder.Out = out
		d.st.Folder.Password = "" // kept only as long as it's needed
		d.mu.Unlock()
		d.changed()
		if !m.completed {
			if err := d.save(); err != nil {
				log.Printf("set %s: saving a part: %v", s.set, err)
			}
		}
	}
	s.sweep(out, members)
	log.Printf("set %s: extracted to %s", s.set, out)
}

// sweep deletes the .part of every completed part of a finished Set (or
// names it, when the archive is kept), once the part's record with the
// output folder is on disk, as in release. Called each step of a finished Set:
// a part whose save failed, or whose .part was left by a stop in the middle of
// finish, is dealt with the next time.
func (s *Set) sweep(out string, members map[int]partInfo) {
	keep := members[1].keep == KeepAfter
	for _, m := range members {
		if !m.completed || !exists(m.path) { // one still to complete (queued with all its bytes, say) does this when it does
			continue
		}
		d := m.d
		d.mu.Lock()
		if f := d.st.Folder; f != nil && f.Out == "" {
			f.Out = out
			f.Password = ""
		}
		d.mu.Unlock()
		if err := d.save(); err != nil {
			log.Printf("set %s: saving a part: %v", s.set, err)
			continue
		}
		d.settlePart(m.path, keep)
		if keep {
			d.changed()
		}
	}
}

// giveUp downloads the parts as plain files: the archive can't be extracted
// as a folder. Nothing was freed yet, or this wouldn't have been reached.
func (s *Set) giveUp(run *setRun, members map[int]partInfo, err error) error {
	var u *archive.Unsupported
	if !errors.As(err, &u) {
		return err
	}
	if s.gaveUp != u.Reason { // parts with bytes freed stay on, so this comes again each tick
		s.gaveUp = u.Reason
		log.Printf("set %s: not as a folder: %s", s.set, u.Reason)
	}
	// The run's handles on the .part files block renaming them on Windows.
	if run.parts != nil {
		run.close()
	}
	*run = setRun{}
	for _, m := range members {
		d := m.d
		d.mu.Lock()
		if f := d.st.Folder; f != nil && !f.locked() {
			d.folderOffLocked()
			f.On, f.Note = false, u.Reason+", so its parts are downloaded as files"
		}
		// A part with bytes freed has holes: it is no file of its own.
		path, done := d.partPath(), d.st.Status == StatusCompleted && !d.st.Folder.locked()
		d.mu.Unlock()
		if done {
			d.keepPart(path)
		}
		d.changed()
	}
	s.accepted.Store(false)
	return nil
}

// keepPart gives a completed part kept for its Set its own name after all.
func (d *Download) keepPart(part string) {
	d.mu.Lock()
	dir, name := d.st.Dir, d.st.Filename
	holdOff := time.Now().Before(d.renameNext)
	d.mu.Unlock()
	if holdOff || !exists(part) {
		return
	}
	final := filepath.Join(dir, uniqueName(dir, name, ""))
	if err := os.Rename(part, final); err != nil {
		log.Printf("download %s: naming the part: %v", d.ID(), err)
		d.mu.Lock()
		d.renameFails++
		d.renameNext = time.Now().Add(renameDelay(d.renameFails))
		d.mu.Unlock()
		return
	}
	d.mu.Lock()
	d.renameFails = 0
	d.st.Filename = filepath.Base(final)
	d.mu.Unlock()
}

// RestartCost is the bytes an engine restart now would download again (see
// Download.RestartCost).
func (s *Set) RestartCost() int64 { return s.cost.Load() }

// Name is the archive's name: x.7z, x.rar or x.zip.
func (s *Set) Name() string { return s.set }

// renameDelay is the wait before naming a part is tried again after n
// failures in a row: 1 s, doubling, up to 30 s.
func renameDelay(n int) time.Duration {
	return min(time.Second<<min(max(n-1, 0), 5), 30*time.Second)
}
