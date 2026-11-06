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
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/klauspost/compress/zstd"

	"downloadmanagersquared/engine/internal/archive/deflate64"
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

	s *stream // the large Entry being extracted as its bytes arrive, if any
}

// Result is what one call to Extract did, for the caller to save.
type Result struct {
	Done   []Entry // extracted, checked and renamed into place
	Freed  []Range // their bytes, now freed in the archive file
	Failed []*Failure

	// More: a large Entry stopped after freeing a step of its bytes, with
	// more of them on disk. Save the above, then call again without waiting.
	More bool
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
	// A large Entry left unfinished starts over: what was written of it goes now.
	for i, e := range a.Entries {
		if !e.Dir && !x.done[i] && large(e) {
			os.Remove(x.path(i) + PartSuffix)
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
		isLarge := !e.Dir && large(e)
		if x.done[i] || !isLarge && !x.ready(e, missing) {
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
			var bad string
			var err error
			finished := true
			if isLarge {
				bad, finished, err = x.streamStep(ctx, i, missing, &r)
			} else {
				bad, err = x.extractFile(ctx, e, path)
			}
			if err != nil {
				return r, err
			}
			if bad != "" {
				r.Failed = append(r.Failed, &Failure{Entry: e, Range: Range{e.Start, e.End}, Reason: bad})
				continue
			}
			if !finished {
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
func (x *Extractor) ready(e Entry, missing []Range) bool { return onDisk(missing, e.Start, e.End) }

// onDisk: none of [start, end) is in missing, which merge has sorted.
func onDisk(missing []Range, start, end int64) bool { return onDiskTo(missing, start) >= end }

// onDiskTo is where the bytes on disk from at on end: the start of the first
// missing range at or after at, or at itself if it is missing.
func onDiskTo(missing []Range, at int64) int64 {
	i, _ := slices.BinarySearchFunc(missing, at, func(r Range, at int64) int { return cmp.Compare(r.End, at+1) })
	if i == len(missing) {
		return math.MaxInt64
	}
	return max(missing[i].Start, at)
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
	data, bad, err := x.header(e)
	if bad != "" || err != nil {
		return bad, err
	}
	out, err := create(path)
	if err != nil {
		return "", err
	}
	bad, err = copyEntry(ctx, e, io.NewSectionReader(x.f, data, e.CompressedSize), out)
	if bad != "" || err != nil {
		discard(out)
		return bad, err
	}
	return "", finish(e, out, path)
}

// header reads the Entry's local header: where its data starts.
func (x *Extractor) header(e Entry) (data int64, bad string, err error) {
	var h [localLen]byte
	if _, err := x.f.ReadAt(h[:], e.Start); err != nil {
		return 0, "", err
	}
	le := binary.LittleEndian
	if le.Uint32(h[:]) != sigLocal {
		return 0, "its header is wrong", nil
	}
	data = e.Start + localLen + int64(le.Uint16(h[26:])) + int64(le.Uint16(h[28:]))
	if data+e.CompressedSize > e.End {
		return 0, "its header is wrong", nil
	}
	return data, "", nil
}

// create makes the unfinished file for path, and the folders it goes in.
func create(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.Create(path + PartSuffix)
}

// discard closes and deletes an unfinished file.
func discard(out *os.File) {
	out.Close()
	os.Remove(out.Name())
}

// zstdMaxWindow is the biggest Zstandard window DMS² decodes with: the
// highest levels (--ultra -22) and long mode (--long) use 128 MiB.
const zstdMaxWindow = 256 << 20

// zstdFits turns down an Entry whose first Zstandard frame needs a window
// over zstdMaxWindow or a dictionary. Anything else wrong with the header is
// left to the decoder, as bytes that came out wrong.
func zstdFits(e Entry, br *bufio.Reader) error {
	b, _ := br.Peek(18) // the longest frame header
	if len(b) < 6 || binary.LittleEndian.Uint32(b) != 0xfd2fb528 {
		return nil // a skippable frame first, or not Zstandard at all
	}
	fhd := b[4]
	single := fhd&0x20 != 0
	dictLen := [4]int{0, 1, 2, 4}[fhd&3]
	fcsLen := [4]int{0, 2, 4, 8}[fhd>>6]
	if fcsLen == 0 && single {
		fcsLen = 1
	}
	at := 5
	var window uint64
	if !single {
		exp, mantissa := uint64(b[5]>>3), uint64(b[5]&7)
		base := uint64(1) << (10 + exp)
		window = base + base/8*mantissa
		at++
	}
	if dictLen > 0 {
		if len(b) < at+dictLen {
			return nil
		}
		var id uint32
		for i := dictLen - 1; i >= 0; i-- {
			id = id<<8 | uint32(b[at+i])
		}
		if id != 0 {
			return unsupported("%s needs a Zstandard dictionary, which isn't in the archive", e.Name)
		}
		at += dictLen
	}
	if single {
		if len(b) < at+fcsLen {
			return nil
		}
		for i := fcsLen - 1; i >= 0; i-- {
			window = window<<8 | uint64(b[at+i])
		}
		if fcsLen == 2 {
			window += 256
		}
	}
	if window > zstdMaxWindow {
		return unsupported("%s was compressed with a %d MiB Zstandard window, more than the %d MiB DMS² extracts with", e.Name, window>>20, zstdMaxWindow>>20)
	}
	return nil
}

// copyEntry decompresses the Entry's data from src into out and checks its
// size and CRC32. bad is why its bytes are wrong; err is any other failure.
func copyEntry(ctx context.Context, e Entry, src io.Reader, out *os.File) (bad string, err error) {
	switch e.Method {
	case Deflate:
		fr := flate.NewReader(src)
		defer fr.Close()
		src = fr
	case Deflate64:
		fr := deflate64.NewReader(src)
		defer fr.Close()
		src = fr
	case Zstd, zstdOld:
		// What the decoder would only report as an error like any other:
		// bytes that are right but can't be decoded here. Fetching them
		// again wouldn't help, so the download stops and says why.
		br := bufio.NewReader(src)
		if err := zstdFits(e, br); err != nil {
			return "", err
		}
		// One goroutine-free decoder, and no bigger window than
		// zstdMaxWindow: a frame can ask for gigabytes.
		zr, err := zstd.NewReader(br, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true),
			zstd.WithDecoderMaxWindow(zstdMaxWindow))
		if err != nil {
			return "", err
		}
		defer zr.Close()
		src = zr
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
	return "", nil
}

// finish puts a checked Entry's file in place, with its time and read-only
// flag. The unfinished file is gone either way.
func finish(e Entry, out *os.File, path string) error {
	tmp := out.Name()
	err := out.Close()
	if err == nil {
		err = os.Chtimes(tmp, e.Modified, e.Modified)
	}
	if err == nil {
		// A file left from an attempt whose end wasn't saved may be read-only.
		if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o200 == 0 {
			os.Chmod(path, 0o666)
		}
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if e.ReadOnly {
		return os.Chmod(path, 0o444)
	}
	return nil
}

// fileError: err came from reading the archive file, not from its bytes being
// wrong. flate wraps those in a ReadError, which doesn't unwrap.
func fileError(err error) bool {
	if errors.Is(err, errStopped) {
		return true
	}
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
