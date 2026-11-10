package archive

import (
	"context"
	"errors"
	"io"
	"os"
)

// LargeEntry: an Entry with more compressed bytes than this is extracted as
// its bytes arrive in order from its front, not once they are all there, and
// the bytes its decompressor has passed are freed as it goes. Tests lower it.
var LargeEntry int64 = 1 << 30

// FreeStep is how many bytes a large Entry's decompressor passes between
// freeing them. Tests lower it.
var FreeStep int64 = 64 << 20

func large(e Entry) bool { return e.CompressedSize > LargeEntry }

// errStopped ends a large Entry's decompressor when its extraction is dropped.
var errStopped = errors.New("extraction stopped")

// stream is a large Entry being extracted as its bytes arrive. Its compressed
// bytes are fed, in order, through a pipe to a decompressor on its own
// goroutine, which keeps its state between calls to Extract.
type stream struct {
	i     int
	path  string
	data  int64    // where the Entry's compressed data starts
	fed   int64    // bytes before this have been handed to the decompressor
	freed int64    // the Entry's bytes before this are freed (from its Start)
	file  *os.File // the unfinished file
	pw    *io.PipeWriter
	end   chan streamEnd
}

// streamEnd is how the decompressor ended: as copyEntry says.
type streamEnd struct {
	bad string
	err error
}

// streamStep extracts what it can of large Entry i: it starts once the
// Entry's header is on disk, then feeds it the bytes on disk in order, and
// frees them behind it every FreeStep, returning after each so they are saved.
// finished is true once the Entry is checked and in place. One large Entry is
// under way at a time: another waits.
func (x *Extractor) streamStep(ctx context.Context, i int, missing []Range, r *Result) (bad string, finished bool, err error) {
	e := x.a.Entries[i]
	if x.s != nil && x.s.i != i {
		return "", false, nil
	}
	if x.s == nil {
		if !onDisk(missing, e.Start, min(e.End, e.Start+localLen)) {
			return "", false, nil
		}
		data, bad, err := x.header(e)
		if bad != "" || err != nil || !onDisk(missing, e.Start, data) {
			return bad, false, err
		}
		if err := x.startStream(i, data); err != nil {
			return "", false, err
		}
	}
	s := x.s
	dataEnd := s.data + e.CompressedSize
	to := min(onDiskTo(missing, s.fed), dataEnd)
	buf := make([]byte, min(1<<20, max(to-s.fed, 0)))
	over := false // the decompressor stopped before taking everything
	for s.fed < to {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		n := min(int64(len(buf)), to-s.fed)
		if _, err := x.f.ReadAt(buf[:n], s.fed); err != nil {
			x.stopStream()
			return "", false, err
		}
		if _, err := s.pw.Write(buf[:n]); err != nil {
			over = true
			break
		}
		s.fed += n
		if s.fed-s.freed >= FreeStep {
			if err := x.f.Free(s.freed, s.fed-s.freed); err != nil {
				x.stopStream()
				return "", false, err
			}
			r.Freed = append(r.Freed, Range{s.freed, s.fed})
			s.freed = s.fed
			// Back to the caller, to save what was freed before more is:
			// bytes freed but not saved would be taken as on disk after a
			// crash. It calls again at once (Result.More).
			r.More = true
			return "", false, nil
		}
	}
	if !over && s.fed < dataEnd {
		return "", false, nil // waits for more of its bytes
	}
	s.pw.Close()
	end := <-s.end
	x.s = nil
	out := s.file
	if end.bad != "" || end.err != nil {
		discard(out)
		return end.bad, false, end.err
	}
	if err := finish(e, out, s.path, x.Mark); err != nil {
		return "", false, err
	}
	return "", true, nil
}

// startStream creates large Entry i's file and starts its decompressor.
func (x *Extractor) startStream(i int, data int64) error {
	e := x.a.Entries[i]
	path := x.path(i)
	out, err := create(path)
	if err != nil {
		return err
	}
	pr, pw := io.Pipe()
	s := &stream{i: i, path: path, data: data, fed: data, freed: e.Start, file: out, pw: pw, end: make(chan streamEnd, 1)}
	go func() {
		bad, err := copyEntry(context.Background(), e, pr, out)
		// A feed still writing gets an error, rather than waiting for ever.
		pr.CloseWithError(errStopped)
		s.end <- streamEnd{bad, err}
	}()
	x.s = s
	return nil
}

// stopStream drops the large Entry under way: its decompressor stops and its
// unfinished file goes. It starts over from its front next time.
func (x *Extractor) stopStream() {
	s := x.s
	if s == nil {
		return
	}
	x.s = nil
	s.pw.CloseWithError(errStopped)
	<-s.end
	discard(s.file)
}

// Close stops extracting: a large Entry under way is dropped, to start over
// from its front with the next Extractor. Its bytes freed so far were
// reported, and must be fetched again first.
func (x *Extractor) Close() {
	x.stopStream()
	if x.z != nil {
		x.stopBlock()
	}
}
