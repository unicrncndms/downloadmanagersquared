package archive

import (
	"io"
	"io/fs"
	"path"
	"time"
)

// OpenEnd ends the one Entry of a RAR in volumes: the archive's size is only
// known once the decompressor reaches its last volume.
const OpenEnd = 1 << 62

// ReadVolumes reads a RAR in volumes (x.part1.rar…, or x.rar, x.r00…) laid
// out end to end in p, from part 1's front. More parts may be laid out as
// their downloads are known (Parts.Grow): the decompressor waits for them.
// A RAR of one part that isn't a volume reads the same way.
func ReadVolumes(p *Parts, password string) (*Archive, error) {
	first := p.Span(0).End
	r := io.NewSectionReader(p, 0, first)
	v := rarVersion(r)
	if v == 0 {
		return nil, errNotRar
	}
	a, err := readRar(r, first, v, password, true)
	if err != nil {
		return nil, err
	}
	known := p.total()
	e := &a.Entries[0]
	// Always streamed: its end is open, so it is never "all on disk".
	e.End, e.CompressedSize, e.Size = OpenEnd, OpenEnd, known
	a.DirStart, a.Total, a.LargestCompressed, a.volumes = OpenEnd, known, 2*FreeStep, p
	if a.encrypted {
		a.password = password
	}
	return a, nil
}

// volumeFS is the parts as rardecode opens its volumes: by the part number in
// the name it asks for, read through the gate.
type volumeFS struct {
	p *Parts
	g *gate
}

func (v *volumeFS) Open(name string) (fs.File, error) {
	_, n, ok := PartOf(path.Base(name))
	if !ok || n < 1 {
		return nil, fs.ErrNotExist
	}
	// shortcut: polls for a part not laid out yet; a channel from Grow if it ever shows.
	for v.p.Len() < n {
		v.p.waiting.Store(int32(n))
		if !v.g.idle() {
			return nil, errStopped
		}
		time.Sleep(50 * time.Millisecond)
	}
	v.p.waiting.CompareAndSwap(int32(n), 0)
	s := v.p.Span(n - 1)
	return &volumeFile{io.NewSectionReader(v.g, s.Start, s.End-s.Start), name}, nil
}

type volumeFile struct {
	*io.SectionReader
	name string
}

func (f *volumeFile) Stat() (fs.FileInfo, error) { return volumeInfo{f}, nil }
func (f *volumeFile) Close() error               { return nil }

type volumeInfo struct{ f *volumeFile }

func (i volumeInfo) Name() string       { return path.Base(i.f.name) }
func (i volumeInfo) Size() int64        { return i.f.Size() }
func (i volumeInfo) Mode() fs.FileMode  { return 0o444 }
func (i volumeInfo) ModTime() time.Time { return time.Time{} }
func (i volumeInfo) IsDir() bool        { return false }
func (i volumeInfo) Sys() any           { return nil }
