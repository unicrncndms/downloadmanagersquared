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

	view     atomic.Pointer[SetSummary]
	accepted atomic.Bool // the archive's list of files has been read: completed parts keep their .part
	finished atomic.Bool // all extracted: no part's .part is needed

	cancel context.CancelFunc
	done   chan struct{}
}

// SetSummary is what the parts' summaries show of their Set.
type SetSummary struct {
	Parts    int    `json:"parts,omitempty"`   // how many the archive has, once known
	Missing  []int  `json:"missing,omitempty"` // parts not added, by number
	Note     string `json:"note,omitempty"`    // "Part 3 missing", or why it isn't extracted
	Files    int    `json:"files"`
	Of       int    `json:"of"`
	Out      string `json:"out,omitempty"`
	Finished bool   `json:"finished,omitempty"`
}

// Splittable says whether a file is a part of a split archive that a Set
// extracts: the set's name and the part's number.
// A plain x.rar is only part 1 of a set beside an x.r00 (see SetOf), and
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

// SetOf is the Set a download belongs to: its key ("" for none), the
// archive's name and the part's number. lone is a plain x.rar or x.zip: part
// 1 or the last part of the set only when other parts of it (x.r00…, x.z01…)
// are there.
func SetOf(d *Download) (key, set string, part int, lone bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if set, part, ok := Splittable(d.st.Filename); ok {
		return archive.SetKey(d.st.Dir, set), set, part, false
	}
	switch strings.ToLower(filepath.Ext(d.st.Filename)) {
	case ".rar":
		return archive.SetKey(d.st.Dir, d.st.Filename), d.st.Filename, 1, true
	case ".zip":
		return archive.SetKey(d.st.Dir, d.st.Filename), d.st.Filename, archive.LastPart, true
	}
	return "", "", 0, false
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

// Start extracts in the background until Stop.
func (s *Set) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	go s.loop(ctx)
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
	parts    *archive.Parts
	files    []*os.File // the Set's own handles, by part index
	released []bool     // the part's .part is deleted: all of it is freed
	need     int64
	arc      *archive.Archive
	x        *archive.Extractor
	out      string
	files1   int // files extracted and in the archive, kept once the Extractor is closed
	of       int
	failed   map[int64]bool
	finished bool
	note     string
	disks    int // a split ZIP's parts, once its .zip says
}

func (r *setRun) close() {
	if r.x != nil {
		r.x.Close()
		r.x = nil
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
	run := &setRun{failed: map[int64]bool{}}
	defer func() {
		if run.parts != nil {
			run.close()
		}
		close(s.done)
	}()
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		more, err := s.step(ctx, run)
		if err != nil && ctx.Err() == nil {
			log.Printf("set %s: %v", s.set, err)
			run.note = err.Error()
			if run.parts != nil {
				run.close()
			}
			run = &setRun{failed: map[int64]bool{}, note: run.note}
		}
		s.publish(run)
		if !more {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}
}

// partInfo is what the Set reads of a part, under its lock.
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
}

func (d *Download) partInfo() partInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := partInfo{d: d, size: d.st.Size, probed: d.st.Probed, completed: d.st.Status == StatusCompleted,
		path: d.partPath(), allocated: d.st.Allocated, name: d.st.Filename}
	if f := d.st.Folder; f != nil {
		p.on, p.into, p.freed, p.done, p.out, p.note = f.On, f.Into, slices.Clone(f.Freed), slices.Clone(f.Done), f.Out, f.Note
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

// step does what can be done now. more: call again at once.
func (s *Set) step(ctx context.Context, run *setRun) (more bool, err error) {
	s.mu.Lock()
	members := make(map[int]partInfo, len(s.members))
	for n, d := range s.members {
		members[n] = d.partInfo()
	}
	s.mu.Unlock()
	run.note = ""
	if m, ok := members[archive.LastPart]; ok && run.disks > 0 {
		delete(members, archive.LastPart) // a split ZIP's .zip: its last part
		members[run.disks] = m
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
		p1.d.env.Changed(p1.d)
	}
	switch {
	case run.finished:
		return false, nil
	case !ok:
		run.note = "Part 1 missing"
		return false, nil
	case !p1.on || !p1.probed:
		return false, nil
	}
	s.follow(p1, members)

	cut := s.zip && strings.HasPrefix(strings.ToLower(p1.name), strings.ToLower(s.set)+".")
	switch {
	case s.rar, cut && run.arc == nil:
		s.layVolumes(run, members)
	case s.zip && run.parts == nil:
		if err := s.layZip(run, members); err != nil {
			return false, s.giveUp(members, err)
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
			return false, s.giveUp(members, err)
		}
		run.parts = archive.NewParts(sizes)
		run.files = make([]*os.File, len(sizes))
		run.released = make([]bool, len(sizes))
		// The list of files is at the end: guess within the last part, so the
		// one before it needn't be there; Need widens it if it must.
		last := run.parts.Span(len(sizes) - 1)
		run.need = max(min(archive.TailGuess, last.End-last.Start), 1)
	}
	n := run.parts.Len()
	for k, m := range members {
		if s.rar || cut {
			break // laid out as they come
		}
		if k > n {
			return false, s.giveUp(members, &archive.Unsupported{Reason: fmt.Sprintf("Part %d is more than the archive has (%d)", k, n)})
		}
		if want := run.parts.Span(k - 1); m.probed && m.size != want.End-want.Start {
			return false, s.giveUp(members, &archive.Unsupported{Reason: fmt.Sprintf("Part %d is %s, not the %s the archive needs", k, human(m.size), human(want.End-want.Start))})
		}
	}

	// The Set's own handles on the parts that have a .part.
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

	// What isn't on disk, in the whole archive.
	var missing []archive.Range
	var gone []int
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
	if len(gone) > 0 {
		run.note = partsMissing(gone)
	}

	if run.arc == nil && s.rar {
		if !onDiskA(missing, 0, min(archive.HeadLen, run.parts.Span(0).End)) {
			return false, nil
		}
		a, err := archive.ReadVolumes(run.parts, "")
		var u *archive.Unsupported
		if errors.As(err, &u) {
			return false, s.giveUp(members, err)
		}
		if err != nil {
			return false, nil
		}
		run.arc = a
		s.accepted.Store(true)
		s.reclaim(run, members, p1.done)
		return true, nil
	}
	if run.arc == nil {
		if run.need == 0 {
			// The list of files is at the end: guess within the last part.
			last := run.parts.Span(n - 1)
			run.need = max(min(archive.TailGuess, last.End-last.Start), 1)
		}
		from := total - run.need
		if !onDiskA(missing, from, total) || !onDiskA(missing, 0, 32) {
			if last, ok := members[n]; ok {
				span := run.parts.Span(n - 1)
				last.d.preferTail(max(from-span.Start, 0))
			}
			return false, nil
		}
		need, err := archive.Need(run.parts, total, run.need)
		if err == nil && need > run.need {
			run.need = need
			return true, nil
		}
		var a *archive.Archive
		if err == nil {
			a, err = archive.Read(run.parts, total)
		}
		var u *archive.Unsupported
		if errors.As(err, &u) && cut && wholePiece(run.parts) {
			// A cut ZIP whose last part known is a whole piece: more may follow.
			if _, ok := members[n+1]; !ok {
				run.note = partsMissing([]int{n + 1})
			}
			return false, nil
		}
		if errors.As(err, &u) {
			return false, s.giveUp(members, err)
		}
		if err != nil {
			return false, nil // its tail isn't all there yet
		}
		run.arc = a
		s.accepted.Store(true)
		s.reclaim(run, members, p1.done)
		return true, nil // what is on disk changed: look again
	}
	if p1.into == "" {
		return false, nil // part 1's card asks where to extract
	}

	if run.x == nil {
		out := p1.out
		if out == "" {
			if out, err = s.nameOut(run.arc, p1); err != nil {
				return false, err
			}
		} else if !exists(out) {
			return false, errFolderRemoved
		}
		if err := checkSpace(p1.into, spaceNeeded(run.arc, p1.done)); err != nil {
			return false, err
		}
		x, err := archive.NewExtractorFrom(run.arc, run.parts, out, p1.done)
		if err != nil {
			return false, s.giveUp(members, err)
		}
		x.Mark = p1.d.markFunc()
		run.x, run.out = x, out
	}

	res, xerr := run.x.Extract(ctx, missing)
	s.record(run, members, res)
	if xerr != nil {
		return false, xerr
	}
	if run.x.Finished() {
		run.finished = true
		s.finish(run, members)
		return false, nil
	}
	s.release(run, members)
	return res.More, nil
}

// layVolumes lays out a RAR's volumes known so far: each after the last, once
// its download knows its size, with none missing before it.
func (s *Set) layVolumes(run *setRun, members map[int]partInfo) {
	k := 0
	if run.parts != nil {
		k = run.parts.Len()
	}
	var add []int64
	for n := k + 1; ; n++ {
		m, ok := members[n]
		if !ok || !m.probed || m.size <= 0 {
			break
		}
		add = append(add, m.size)
	}
	if len(add) == 0 {
		return
	}
	run.need = 0 // the last part changed
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

func onDiskA(missing []archive.Range, start, end int64) bool {
	for _, r := range missing {
		if r.Start < end && start < r.End {
			return false
		}
	}
	return true
}

// follow has every part take part 1's switch and folder: the Set is one
// download as folder.
func (s *Set) follow(p1 partInfo, members map[int]partInfo) {
	for n, m := range members {
		if n == 1 || m.on && m.into == p1.into {
			continue
		}
		d := m.d
		d.mu.Lock()
		if d.st.Folder == nil {
			d.st.Folder = &Folder{}
		}
		if !d.st.Folder.locked() {
			// The Note goes too: one read alone before its set was known says it is a part of one.
			d.st.Folder.On, d.st.Folder.Auto, d.st.Folder.Into, d.st.Folder.Note = true, false, p1.into, ""
			if !d.st.Allocated {
				d.st.Dir = p1.into
			}
		}
		d.mu.Unlock()
		d.env.Changed(d)
	}
}

// preferTail has a running download fetch from `at` on first.
func (d *Download) preferTail(at int64) {
	d.mu.Lock()
	if d.plan != nil && d.plan.prefer != at {
		d.plan.cut(at)
		d.plan.prefer = at
	}
	d.mu.Unlock()
}

// nameOut makes the output folder, named after the archive's Root or the set.
func (s *Set) nameOut(a *archive.Archive, p1 partInfo) (string, error) {
	base := a.Root
	if base == "" {
		base = strings.TrimSuffix(s.set, filepath.Ext(s.set))
	}
	if strings.TrimSpace(base) == "" {
		base = "download"
	}
	if err := os.MkdirAll(p1.into, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(p1.into, uniqueName(p1.into, base, ""))
	if err := os.Mkdir(out, 0o755); err != nil {
		return "", err
	}
	d := p1.d
	d.mu.Lock()
	d.st.Folder.Out = out
	d.mu.Unlock()
	d.env.Changed(d)
	return out, nil
}

// reclaim fetches again the bytes freed of blocks not done: a block stopped
// partway is extracted again from its front.
func (s *Set) reclaim(run *setRun, members map[int]partInfo, done []int64) {
	was := map[int64]bool{}
	for _, st := range done {
		was[st] = true
	}
	for _, e := range run.arc.Entries {
		if was[e.Start] {
			continue
		}
		s.refetch(run, members, archive.Range{Start: e.Start, End: e.End}, true)
	}
}

// refetch has the parts fetch r again: only what was freed of it, or all of it.
func (s *Set) refetch(run *setRun, members map[int]partInfo, r archive.Range, freedOnly bool) {
	parts, local := run.parts.Cut(r)
	for k, i := range parts {
		m, ok := members[i+1]
		if !ok || run.released[i] {
			continue
		}
		want := []Range{{local[k].Start, local[k].End}}
		if freedOnly {
			want = intersectRanges(want, m.freed)
		}
		for _, w := range want {
			m.d.refetch(w)
		}
	}
}

// refetch puts r back to be fetched: a completed part is downloaded again.
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
	log.Printf("download %s: fetching %d-%d again for its set", d.ID(), r.Start, r.End)
	d.env.Changed(d)
}

func intersectRanges(a, b []Range) []Range {
	var out []Range
	for _, x := range a {
		for _, y := range b {
			if lo, hi := max(x.Start, y.Start), min(x.End, y.End); lo < hi {
				out = append(out, Range{lo, hi})
			}
		}
	}
	return out
}

// record saves what Extract did: the blocks done on part 1, the bytes freed
// on the part they are in, and wrong blocks fetched again.
func (s *Set) record(run *setRun, members map[int]partInfo, res archive.Result) {
	p1 := members[1].d
	files, of := run.x.Progress()
	p1.mu.Lock()
	f := p1.st.Folder
	for _, e := range res.Done {
		f.Done = append(f.Done, e.Start)
	}
	f.Files, f.Of = files, of
	p1.mu.Unlock()
	if len(res.Done) > 0 || f.Files != files {
		p1.env.Changed(p1)
	}
	for _, r := range res.Freed {
		parts, local := run.parts.Cut(r)
		for k, i := range parts {
			m, ok := members[i+1]
			if !ok {
				continue
			}
			m.d.mu.Lock()
			mf := m.d.st.Folder
			mf.Freed = mergeRanges(append(mf.Freed, Range{local[k].Start, local[k].End}))
			m.d.mu.Unlock()
			m.d.env.Changed(m.d)
		}
	}
	for _, fl := range res.Failed {
		if run.failed[fl.Range.Start] {
			run.note = fmt.Sprintf("%s came out wrong twice (%s)", fl.Entry.Name, fl.Reason)
		}
		run.failed[fl.Range.Start] = true
		log.Printf("set %s: %s, fetching it again", s.set, fl)
		s.refetch(run, members, fl.Range, false)
	}
}

// release deletes the .part of every completed part that is all freed.
func (s *Set) release(run *setRun, members map[int]partInfo) {
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
	for k, e := range r.arc.Entries {
		if e.Start < span.End && span.Start < e.End && !r.x.Done(k) {
			return false
		}
	}
	// The list of files sits at the end of the last part: once read, it isn't needed again.
	return true
}

func sumRanges(rs []Range) int64 {
	var n int64
	for _, r := range rs {
		n += r.End - r.Start
	}
	return n
}

// finish deletes every part's .part: all is extracted.
func (s *Set) finish(run *setRun, members map[int]partInfo) {
	out := run.out
	run.files1, run.of = run.x.Progress()
	run.close()
	s.finished.Store(true)
	for _, m := range members {
		d := m.d
		d.mu.Lock()
		d.st.Folder.Out = out
		os.Remove(m.path) // fails while the part is still downloading: it removes it when it completes
		d.mu.Unlock()
		d.env.Changed(d)
	}
	log.Printf("set %s: extracted to %s", s.set, out)
}

// giveUp downloads the parts as plain files: the archive can't be extracted
// as a folder. Nothing was freed yet, or this wouldn't have been reached.
func (s *Set) giveUp(members map[int]partInfo, err error) error {
	var u *archive.Unsupported
	if !errors.As(err, &u) {
		return err
	}
	log.Printf("set %s: not as a folder: %s", s.set, u.Reason)
	for _, m := range members {
		d := m.d
		d.mu.Lock()
		if f := d.st.Folder; f != nil && !f.locked() {
			d.folderOffLocked()
			f.On, f.Note = false, u.Reason+", so its parts are downloaded as files"
		}
		path, done := d.partPath(), d.st.Status == StatusCompleted
		d.mu.Unlock()
		if done {
			d.keepPart(path)
		}
		d.env.Changed(d)
	}
	s.accepted.Store(false)
	return nil
}

// keepPart gives a completed part kept for its Set its own name after all.
func (d *Download) keepPart(part string) {
	d.mu.Lock()
	dir, name := d.st.Dir, d.st.Filename
	d.mu.Unlock()
	if !exists(part) {
		return
	}
	final := filepath.Join(dir, uniqueName(dir, name, ""))
	if err := os.Rename(part, final); err != nil {
		log.Printf("download %s: naming the part: %v", d.ID(), err)
		return
	}
	d.mu.Lock()
	d.st.Filename = filepath.Base(final)
	d.mu.Unlock()
}

// publish updates what the parts' summaries show.
func (s *Set) publish(run *setRun) {
	v := &SetSummary{Note: run.note, Out: run.out, Finished: run.finished}
	if run.parts != nil {
		v.Parts = run.parts.Len()
		s.mu.Lock()
		for i := 1; i <= v.Parts; i++ {
			if s.members[i] == nil && !(i == v.Parts && s.members[archive.LastPart] != nil) {
				v.Missing = append(v.Missing, i)
			}
		}
		s.mu.Unlock()
	}
	v.Files, v.Of = run.files1, run.of
	if run.x != nil {
		v.Files, v.Of = run.x.Progress()
	}
	old := s.view.Load()
	if old == nil || fmt.Sprint(*old) != fmt.Sprint(*v) {
		s.view.Store(v)
		s.mu.Lock()
		for _, d := range s.members {
			d.env.Changed(d)
		}
		s.mu.Unlock()
	}
}

// Summary is what the parts show of the Set, nil before it has run.
func (s *Set) Summary() *SetSummary { return s.view.Load() }
