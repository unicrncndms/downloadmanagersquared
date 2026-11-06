package archive

import (
	"bufio"
	"cmp"
	"compress/flate"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"downloadmanagersquared/engine/internal/winutil"
)

// Range is the bytes [Start, End) of the archive file.
type Range struct{ Start, End int64 }

// PartSuffix marks a file still being extracted.
const PartSuffix = ".dms.part"

// freeReach is how far freeing an Entry may reach into a done neighbour's
// bytes, so the allocation unit they share is freed too (NTFS frees whole
// units: 64 KB, more on disks with large clusters).
const freeReach = 1 << 20

// Extractor extracts an archive's Entries out of the file it is downloading
// into, each as soon as all its bytes are there, and frees their bytes in
// that file once they are. The file must be sparse (winutil.SetSparse).
//
// The output folder stands for the archive's Root when it has one: an Entry
// "Game/bin/x.exe" of an archive with Root "Game" goes to <dir>/bin/x.exe, so
// the caller names dir after Root (numbered if taken) and it isn't nested
// twice. Without a Root, Entries go under dir as they are.
//
// An Extractor is not safe for use by several goroutines at once.
type Extractor struct {
	a         *Archive
	f         *os.File
	dir       string
	rel       []string // per Entry: its path under dir, '/'-separated; "" is dir itself
	done      []bool
	files     int // Entries that are files
	extracted int // of those, done
	first     int // Entries before this are all done

	folders map[string]int // folder Entry path -> its index
}

// Result is what one call to Extract did, for the caller to save.
type Result struct {
	Done   []Entry // extracted, checked and renamed into place
	Freed  []Range // their bytes, now freed in the archive file
	Failed []*Failure
}

// Failure is an Entry whose bytes are wrong: its CRC32 or size doesn't match
// the central directory, or its data can't be read. Nothing of it is kept and
// its bytes aren't freed. The Extractor doesn't try it again on its own: it is
// extracted again the next time Extract is called while its whole Range is
// on disk, so the caller fetches that Range again first.
type Failure struct {
	Entry  Entry
	Range  Range
	Reason string
}

func (f *Failure) Error() string { return fmt.Sprintf("%s: %s", f.Entry.Name, f.Reason) }

// NewExtractor extracts a's Entries from f into dir. done lists the Start of
// every Entry already extracted before: those are never checked or written
// again, whatever has happened to their files since.
func NewExtractor(a *Archive, f *os.File, dir string, done []int64) (*Extractor, error) {
	x := &Extractor{a: a, f: f, dir: dir, done: make([]bool, len(a.Entries)), folders: map[string]int{}}
	was := map[int64]bool{}
	for _, s := range done {
		was[s] = true
	}
	names := map[string]bool{}
	for i, e := range a.Entries {
		rel := strings.TrimSuffix(e.Name, "/")
		if a.Root != "" {
			// Every Entry's first element is the Root, maybe in other case.
			_, rel, _ = strings.Cut(rel, "/")
		}
		x.rel = append(x.rel, rel)
		// Its folders too, as they'd be made for it.
		for p := strings.ToLower(rel); p != ""; p = parent(p) {
			names[p] = true
		}
		x.done[i] = was[e.Start]
		if e.Dir {
			x.folders[rel] = i
			continue
		}
		x.files++
		if x.done[i] {
			x.extracted++
		}
	}
	for i, e := range a.Entries {
		if !e.Dir && names[strings.ToLower(x.rel[i]+PartSuffix)] {
			return nil, unsupported("A file in it is named like the files DMS² hasn't finished extracting (%s)", e.Name+PartSuffix)
		}
	}
	x.skipDone()
	return x, nil
}

// parent is the folder p is in, "" for the top.
func parent(p string) string {
	if j := strings.LastIndexByte(p, '/'); j >= 0 {
		return p[:j]
	}
	return ""
}

// skipDone moves first past the Entries done.
func (x *Extractor) skipDone() {
	for x.first < len(x.done) && x.done[x.first] {
		x.first++
	}
}

// Progress is how many of the archive's files are extracted.
func (x *Extractor) Progress() (files, of int) { return x.extracted, x.files }

// Finished: every Entry is done.
func (x *Extractor) Finished() bool { return x.first == len(x.done) }

// Extract extracts every Entry not done yet whose bytes are all on disk, in
// archive order. missing is what isn't on disk yet, as the download's plan
// has it. It stops at the first error that isn't the Entry's own fault (the
// disk, the output folder, ctx), returning what it did up to then.
func (x *Extractor) Extract(ctx context.Context, missing []Range) (Result, error) {
	missing = merge(missing)
	var r Result
	touched := map[string]bool{} // folders written into
	defer x.restoreFolderTimes(touched)
	defer x.skipDone()
	for i := x.first; i < len(x.a.Entries); i++ {
		e := x.a.Entries[i]
		if x.done[i] || !x.ready(e, missing) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return r, err
		}
		path := x.path(i)
		if e.Dir {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return r, err
			}
			touched[x.rel[i]] = true
		} else {
			bad, err := x.extractFile(ctx, e, path)
			if err != nil {
				return r, err
			}
			if bad != "" {
				r.Failed = append(r.Failed, &Failure{Entry: e, Range: Range{e.Start, e.End}, Reason: bad})
				continue
			}
		}
		for p := x.rel[i]; p != ""; {
			p = parent(p)
			touched[p] = true
		}
		// Done only once its bytes are freed: an Entry done is never looked
		// at again, so its bytes would stay. If freeing fails, it is
		// extracted again next time.
		x.done[i] = true
		if err := x.free(i); err != nil {
			x.done[i] = false
			return r, err
		}
		if !e.Dir {
			x.extracted++
		}
		r.Done = append(r.Done, e)
		r.Freed = append(r.Freed, Range{e.Start, e.End})
	}
	return r, nil
}

// merge sorts ranges and joins those that overlap or touch, so ready can
// search them.
func merge(rs []Range) []Range {
	rs = slices.Clone(rs)
	slices.SortFunc(rs, func(a, b Range) int { return cmp.Compare(a.Start, b.Start) })
	out := rs[:0]
	for _, r := range rs {
		if r.End <= r.Start {
			continue
		}
		if n := len(out); n > 0 && r.Start <= out[n-1].End {
			out[n-1].End = max(out[n-1].End, r.End)
			continue
		}
		out = append(out, r)
	}
	return out
}

// ready: every byte of the Entry is on disk.
//
// Ticket 05 goes here: an Entry above a size threshold becomes ready once its
// front is on disk, and is extracted as its bytes arrive in order.
func (x *Extractor) ready(e Entry, missing []Range) bool {
	i, _ := slices.BinarySearchFunc(missing, e.Start, func(r Range, at int64) int { return cmp.Compare(r.End, at+1) })
	return i == len(missing) || missing[i].Start >= e.End
}

func (x *Extractor) path(i int) string {
	return filepath.Join(x.dir, filepath.FromSlash(x.rel[i]))
}

// free frees Entry i's bytes, reaching up to freeReach into the done Entries
// around it, so that the allocation units they share go too.
func (x *Extractor) free(i int) error {
	es := x.a.Entries
	lo, hi := es[i].Start, es[i].End
	for j := i - 1; j >= 0 && x.done[j] && lo > es[i].Start-freeReach; j-- {
		lo = max(es[j].Start, es[i].Start-freeReach)
	}
	for j := i + 1; j < len(es) && x.done[j] && hi < es[i].End+freeReach; j++ {
		hi = min(es[j].End, es[i].End+freeReach)
	}
	return winutil.FreeRange(x.f, lo, hi-lo)
}

// extractFile writes the Entry to path. bad is why its bytes are wrong; err is
// any other failure.
func (x *Extractor) extractFile(ctx context.Context, e Entry, path string) (bad string, err error) {
	var h [localLen]byte
	if _, err := x.f.ReadAt(h[:], e.Start); err != nil {
		return "", err
	}
	le := binary.LittleEndian
	if le.Uint32(h[:]) != sigLocal {
		return "its header is wrong", nil
	}
	data := e.Start + localLen + int64(le.Uint16(h[26:])) + int64(le.Uint16(h[28:]))
	if data+e.CompressedSize > e.End {
		return "its header is wrong", nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp := path + PartSuffix
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	keep := false
	defer func() {
		if !keep {
			out.Close()
			os.Remove(tmp)
		}
	}()

	var src io.Reader = io.NewSectionReader(x.f, data, e.CompressedSize)
	if e.Method == Deflate {
		fr := flate.NewReader(src)
		defer fr.Close()
		src = fr
	}
	// One byte more than it should have shows a size that's too big, without
	// writing more than that.
	src = io.LimitReader(src, e.Size+1)
	sum := crc32.NewIEEE()
	w := bufio.NewWriterSize(io.MultiWriter(out, sum), 256<<10)
	buf := make([]byte, 256<<10)
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		k, rerr := src.Read(buf)
		if k > 0 {
			if _, err := w.Write(buf[:k]); err != nil {
				return "", err
			}
			n += int64(k)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if fileError(rerr) {
				return "", rerr // reading the archive file failed, not its bytes
			}
			return "its data can't be decompressed", nil
		}
	}
	if err := w.Flush(); err != nil {
		return "", err
	}
	switch {
	case n != e.Size:
		return fmt.Sprintf("it came out %d bytes long instead of %d", n, e.Size), nil
	case sum.Sum32() != e.CRC32:
		return "its checksum doesn't match", nil
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	if err := os.Chtimes(tmp, e.Modified, e.Modified); err != nil {
		return "", err
	}
	// A file left from an attempt whose end wasn't saved may be read-only.
	if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o200 == 0 {
		os.Chmod(path, 0o666)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	keep = true
	if e.ReadOnly {
		if err := os.Chmod(path, 0o444); err != nil {
			return "", err
		}
	}
	return "", nil
}

// fileError: err came from reading the archive file, not from its bytes being
// wrong. flate wraps those in a ReadError, which doesn't unwrap.
func fileError(err error) bool {
	var re *flate.ReadError
	if errors.As(err, &re) {
		err = re.Err
	}
	var pe *os.PathError
	return errors.As(err, &pe)
}

// restoreFolderTimes gives the done folder Entries among touched their times
// back, as writing into a folder changes its time.
func (x *Extractor) restoreFolderTimes(touched map[string]bool) {
	for p := range touched {
		if i, ok := x.folders[p]; ok && x.done[i] {
			t := x.a.Entries[i].Modified
			os.Chtimes(x.path(i), t, t)
		}
	}
}
