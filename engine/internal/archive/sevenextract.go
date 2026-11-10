package archive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bodgit/sevenzip"

	"downloadmanagersquared/engine/internal/winutil"
)

// blockExtract is an Extractor's state for a 7z or a RAR. Its Entries are
// blocks: each is decompressed front to back into its files, reading the
// archive file through a gate. A RAR is one block.
type blockExtract struct {
	decode   func(ctx context.Context, i int) (bad string, err error)
	zr       *sevenzip.Reader // a 7z's
	rar      *rarState        // a RAR's
	gate     *gate
	rel      []string // per file: its path under dir, '/'-separated; "" is dir itself
	prepared bool     // folders and empty files made
	timed    bool     // folder times set, once everything is done
	run      *blockRun
}

// blockRun is a large block decompressing on its own goroutine as its bytes
// arrive (see stream for a ZIP's).
type blockRun struct {
	i     int
	freed int64 // the block's bytes before this are freed
	end   chan streamEnd
}

// gate is the archive file as the sevenzip reader sees it. Reads in the
// block under way wait until its bytes up to front are on disk; read is how
// far they have got, so the bytes before it can be freed.
type gate struct {
	f      *os.File
	mu     sync.Mutex
	cond   *sync.Cond
	lo, hi int64 // the block under way, if any
	front  int64
	read   int64
	stop   bool

	waiting bool // the decompressor waits for bytes not let through yet
	ended   bool // the decompressor is done with the block
}

func newGate(f *os.File) *gate {
	g := &gate{f: f}
	g.cond = sync.NewCond(&g.mu)
	return g
}

func (g *gate) ReadAt(p []byte, off int64) (int, error) {
	g.mu.Lock()
	if off >= g.hi || off+int64(len(p)) <= g.lo {
		g.mu.Unlock()
		return g.f.ReadAt(p, off)
	}
	end := min(off+int64(len(p)), g.hi)
	for g.front < end && !g.stop {
		g.waiting = true
		g.cond.Broadcast()
		g.cond.Wait()
		g.waiting = false
	}
	if g.stop {
		g.mu.Unlock()
		return 0, errStopped
	}
	g.mu.Unlock()
	n, err := g.f.ReadAt(p, off)
	// Only once they are read may they be freed.
	g.mu.Lock()
	g.read = max(g.read, min(off+int64(n), end))
	g.mu.Unlock()
	return n, err
}

// open lets a block's bytes through as they arrive, from lo.
func (g *gate) open(lo, hi int64) {
	g.mu.Lock()
	g.lo, g.hi, g.front, g.read, g.stop = lo, hi, lo, lo, false
	g.waiting, g.ended = false, false
	g.mu.Unlock()
}

// end says the decompressor is done with the block.
func (g *gate) end() {
	g.mu.Lock()
	g.ended = true
	g.mu.Unlock()
	g.cond.Broadcast()
}

// advance lets the block's bytes up to front through, and waits until the
// decompressor has taken all it can of them, as a ZIP's pipe does.
func (g *gate) advance(front int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if front > g.front {
		g.front = front
		g.waiting = false
		g.cond.Broadcast()
	}
	for !g.waiting && !g.ended && !g.stop {
		g.cond.Wait()
	}
}

// close lets every read through again, failing those waiting.
func (g *gate) close() {
	g.mu.Lock()
	g.lo, g.hi, g.stop = 0, 0, true
	g.mu.Unlock()
	g.cond.Broadcast()
}

func (g *gate) readTo() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.read
}

func newSevenExtractor(x *Extractor, done []int64) (*Extractor, error) {
	a := x.a
	g := newGate(x.f)
	fi, err := x.f.Stat()
	if err != nil {
		return nil, err
	}
	zr, err := sevenzip.NewReader(g, fi.Size())
	if err != nil || len(zr.File) != len(a.z.files) {
		return nil, errNot7z
	}
	s := &blockExtract{zr: zr, gate: g}
	s.decode = x.decodeBlock
	names := map[string]bool{}
	for _, f := range a.z.files {
		rel := strings.TrimSuffix(f.Name, "/")
		if a.Root != "" {
			_, rel, _ = strings.Cut(rel, "/")
		}
		s.rel = append(s.rel, rel)
		for p := strings.ToLower(rel); p != ""; p = parent(p) {
			names[p] = true
		}
	}
	for i, f := range a.z.files {
		if !f.Dir && names[strings.ToLower(s.rel[i]+PartSuffix)] {
			return nil, unsupported("A file in it is named like the files DMS² hasn't finished extracting (%s)", f.Name+PartSuffix)
		}
	}
	x.z = s
	was := map[int64]bool{}
	for _, st := range done {
		was[st] = true
	}
	for i, e := range a.Entries {
		x.done[i] = was[e.Start]
	}
	for _, f := range a.z.files {
		if f.Dir {
			continue
		}
		x.files++
		if f.block >= 0 && x.done[f.block] {
			x.extracted++
		}
	}
	x.skipDone()
	return x, nil
}

func (s *blockExtract) path(dir string, i int) string {
	return filepath.Join(dir, filepath.FromSlash(s.rel[i]))
}

// prepare makes the folders and the empty files, which have no bytes in the
// archive. An empty file already there is left alone.
func (x *Extractor) prepare7z() error {
	for i, f := range x.a.z.files {
		if f.block >= 0 || x.z.rel[i] == "" {
			continue
		}
		path := x.z.path(x.dir, i)
		if f.Dir {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
			continue
		}
		if _, err := os.Lstat(path); err != nil {
			out, err := create(path)
			if err != nil {
				return err
			}
			if err := finish(f.Entry, out, path, x.Mark); err != nil {
				return err
			}
		}
		x.extracted++
	}
	x.z.prepared = true
	return nil
}

// extract7z is Extract for a 7z: each block once its bytes are on disk, or a
// large one as they arrive.
func (x *Extractor) extract7z(ctx context.Context, missing []Range) (Result, error) {
	var r Result
	defer x.skipDone()
	if !x.z.prepared {
		if err := x.prepare7z(); err != nil {
			return r, err
		}
	}
	for i := x.first; i < len(x.a.Entries); i++ {
		if x.done[i] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return r, err
		}
		e := x.a.Entries[i]
		var bad string
		var err error
		if large(e) && !x.a.z.multi[i] {
			var finished bool
			bad, finished, err = x.blockStep(i, missing, &r)
			if err != nil || r.More {
				return r, err
			}
			if !finished && bad == "" {
				continue
			}
		} else {
			if x.z.run != nil || !onDisk(missing, e.Start, e.End) {
				continue // one block at a time reads through the gate
			}
			bad, err = x.decodeBlock(ctx, i)
			if err != nil {
				return r, err
			}
		}
		if bad != "" {
			r.Failed = append(r.Failed, &Failure{Entry: e, Range: Range{e.Start, e.End}, Reason: bad})
			continue
		}
		x.done[i] = true
		if err := x.free(i); err != nil {
			x.done[i] = false
			return r, err
		}
		x.extracted += len(x.a.z.blocks[i])
		r.Done = append(r.Done, e)
		r.Freed = append(r.Freed, Range{e.Start, e.End})
	}
	if x.Finished() && !x.z.timed {
		x.z.timed = true
		for i, f := range x.a.z.files {
			if f.Dir && x.z.rel[i] != "" {
				os.Chtimes(x.z.path(x.dir, i), f.Modified, f.Modified)
			}
		}
	}
	return r, nil
}

// blockStep goes on with large block i: it starts once its first bytes are
// on disk, lets through those that have arrived since, and frees the bytes
// read behind it every FreeStep, returning after each so they are saved.
func (x *Extractor) blockStep(i int, missing []Range, r *Result) (bad string, finished bool, err error) {
	e := x.a.Entries[i]
	run := x.z.run
	if run != nil && run.i != i {
		return "", false, nil
	}
	if run == nil {
		if onDiskTo(missing, e.Start) == e.Start {
			return "", false, nil
		}
		run = &blockRun{i: i, freed: e.Start, end: make(chan streamEnd, 1)}
		x.z.gate.open(e.Start, e.End)
		x.z.run = run
		go func() {
			bad, err := x.z.decode(context.Background(), i)
			run.end <- streamEnd{bad, err}
			x.z.gate.end()
		}()
	}
	x.z.gate.advance(min(onDiskTo(missing, e.Start), e.End))
	select {
	case end := <-run.end:
		x.z.run = nil
		x.z.gate.close()
		return end.bad, end.bad == "" && end.err == nil, end.err
	default:
	}
	if to := x.z.gate.readTo(); to-run.freed >= FreeStep {
		if err := winutil.FreeRange(x.f, run.freed, to-run.freed); err != nil {
			x.stopBlock()
			return "", false, err
		}
		r.Freed = append(r.Freed, Range{run.freed, to})
		run.freed = to
		r.More = true
	}
	return "", false, nil
}

// stopBlock drops the large block under way: it starts over from its front
// next time.
func (x *Extractor) stopBlock() {
	run := x.z.run
	if run == nil {
		return
	}
	x.z.run = nil
	x.z.gate.close()
	<-run.end
}

// decodeBlock decompresses block i into its files, each checked against its
// size and CRC32 and put in place. bad is why its bytes are wrong.
func (x *Extractor) decodeBlock(ctx context.Context, i int) (bad string, err error) {
	for _, k := range x.a.z.blocks[i] {
		f := x.a.z.files[k]
		rc, err := x.z.zr.File[f.index].Open()
		if err != nil {
			if fileError(err) {
				return "", err
			}
			return "its data can't be decompressed", nil
		}
		path := x.z.path(x.dir, k)
		out, err := create(path)
		if err != nil {
			rc.Close()
			return "", err
		}
		e := f.Entry
		e.Method = Store // decompressed already
		bad, err = copyEntry(ctx, e, rc, out)
		rc.Close()
		if bad != "" || err != nil {
			discard(out)
			return bad, err
		}
		if err := finish(e, out, path, x.Mark); err != nil {
			return "", err
		}
	}
	return "", nil
}
