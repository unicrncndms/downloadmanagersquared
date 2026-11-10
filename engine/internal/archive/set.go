package archive

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// SplitSizes is where each part of a 7z cut into equal pieces (.7z.001,
// .7z.002, …) sits: the size of every part, from the first part's front
// (its start header says how long the whole archive is) and that part's size.
func SplitSizes(front []byte, partSize int64) ([]int64, error) {
	if len(front) < sevenStartLen || string(front[:len(sevenSig)]) != string(sevenSig) {
		return nil, errNot7z
	}
	le := binary.LittleEndian
	total := sevenStartLen + int64(le.Uint64(front[12:])) + int64(le.Uint64(front[20:]))
	if partSize <= 0 || total < sevenStartLen || total > 1<<50 {
		return nil, errNot7z
	}
	var sizes []int64
	for left := total; left > 0; left -= partSize {
		sizes = append(sizes, min(left, partSize))
		if len(sizes) > 10000 {
			return nil, errNot7z
		}
	}
	return sizes, nil
}

// ZipParts is how many parts a split ZIP (.z01, .z02, …, .zip) has, from the
// tail of its .zip: its end record says which disk it is on.
func ZipParts(tail []byte) (int, error) {
	for i := len(tail) - endLen; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:]) == sigEnd {
			return int(binary.LittleEndian.Uint16(tail[i+4:])) + 1, nil
		}
	}
	return 0, errNotZip
}

// MissingPart is a read of a part that isn't there yet.
type MissingPart struct{ Part int } // from 1

func (m *MissingPart) Error() string { return fmt.Sprintf("part %d is missing", m.Part) }

// Parts is a split archive's parts end to end as one File. A part is added
// once its download has a file; reading one that isn't is a *MissingPart.
type Parts struct {
	sizes  []int64
	starts []int64

	mu      sync.Mutex
	files   []File
	waiting atomic.Int32 // the part a RAR's decompressor waits to be laid out
}

// NewParts lays out parts of these sizes, none of them there yet.
func NewParts(sizes []int64) *Parts {
	p := &Parts{sizes: sizes, starts: make([]int64, len(sizes)), files: make([]File, len(sizes))}
	var at int64
	for i, n := range sizes {
		p.starts[i] = at
		at += n
	}
	return p
}

// Grow lays out more parts after the last: those before stay where they are.
func (p *Parts) Grow(sizes ...int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, n := range sizes {
		p.starts = append(p.starts, p.totalLocked())
		p.sizes = append(p.sizes, n)
		p.files = append(p.files, nil)
	}
}

// Waiting is the part a RAR in volumes needs next and isn't laid out (0 for none).
func (p *Parts) Waiting() int { return int(p.waiting.Load()) }

// Set puts part i's file in (nil takes it out).
func (p *Parts) Set(i int, f File) {
	p.mu.Lock()
	p.files[i] = f
	p.mu.Unlock()
}

// Len is how many parts there are.
func (p *Parts) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sizes)
}

// Span is where part i sits in the whole.
func (p *Parts) Span(i int) Range {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Range{p.starts[i], p.starts[i] + p.sizes[i]}
}

func (p *Parts) Size() (int64, error) { return p.total(), nil }

func (p *Parts) total() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.totalLocked()
}

func (p *Parts) totalLocked() int64 {
	if len(p.sizes) == 0 {
		return 0
	}
	return p.starts[len(p.starts)-1] + p.sizes[len(p.sizes)-1]
}

// Cut splits r into the parts it covers, as (part, range within the part).
func (p *Parts) Cut(r Range) (parts []int, local []Range) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.sizes {
		s := Range{p.starts[i], p.starts[i] + p.sizes[i]}
		lo, hi := max(r.Start, s.Start), min(r.End, s.End)
		if lo < hi {
			parts = append(parts, i)
			local = append(local, Range{lo - s.Start, hi - s.Start})
		}
	}
	return parts, local
}

func (p *Parts) ReadAt(b []byte, off int64) (int, error) {
	if off >= p.total() {
		return 0, io.EOF
	}
	n := 0
	parts, local := p.Cut(Range{off, off + int64(len(b))})
	for k, i := range parts {
		p.mu.Lock()
		f := p.files[i]
		p.mu.Unlock()
		if f == nil {
			return n, &MissingPart{i + 1}
		}
		m, err := f.ReadAt(b[n:n+int(local[k].End-local[k].Start)], local[k].Start)
		n += m
		if err != nil {
			return n, err
		}
	}
	if n < len(b) {
		return n, io.EOF
	}
	return n, nil
}

func (p *Parts) Free(off, n int64) error {
	parts, local := p.Cut(Range{off, off + n})
	for k, i := range parts {
		p.mu.Lock()
		f := p.files[i]
		p.mu.Unlock()
		if f == nil {
			continue // nothing of it on disk to free
		}
		if err := f.Free(local[k].Start, local[k].End-local[k].Start); err != nil {
			return err
		}
	}
	return nil
}
