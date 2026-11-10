package archive

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nwaples/rardecode/v2"
)

var (
	rar4Sig = []byte("Rar!\x1a\x07\x00")
	rar5Sig = []byte("Rar!\x1a\x07\x01\x00")
)

// methodRar marks the one Entry of a RAR: the whole archive, which has no
// list of files to read first, decompressed front to back.
const methodRar uint16 = 0xfffe

// rarMaxDict is the largest dictionary DMS² decompresses with: WinRAR's
// default is 32 MB, and the decoder takes this much memory.
const rarMaxDict = 1 << 30

// rarVersion is 4 or 5 for a RAR signature at the front, else 0.
func rarVersion(r io.ReaderAt) int {
	b := make([]byte, len(rar5Sig))
	n, _ := r.ReadAt(b, 0)
	switch {
	case n >= len(rar5Sig) && bytes.Equal(b, rar5Sig):
		return 5
	case n >= len(rar4Sig) && bytes.Equal(b[:len(rar4Sig)], rar4Sig):
		return 4
	}
	return 0
}

var (
	errNotRar = &Unsupported{Reason: "It isn't a RAR file, or its first files can't be read"}
	errRarDict = unsupported("It was packed with a dictionary over %d MB, more than DMS² extracts with", rarMaxDict>>20)
)

// mainFlags reads a RAR's main header: whether it is one volume of several,
// and whether its headers are encrypted.
func mainFlags(front []byte, ver int) (volume, encrypted bool, err error) {
	if ver == 4 {
		// CRC16, type 0x73, flags: 0x0001 volume, 0x0080 encrypted headers.
		h := front[len(rar4Sig):]
		if len(h) < 5 || h[2] != 0x73 {
			return false, false, errNotRar
		}
		f := binary.LittleEndian.Uint16(h[3:])
		return f&0x0001 != 0, f&0x0080 != 0, nil
	}
	h := front[len(rar5Sig):]
	if len(h) < 4 {
		return false, false, errNotRar
	}
	h = h[4:] // CRC32
	vint := func() uint64 {
		var v uint64
		for i := 0; i < 10 && len(h) > 0; i++ {
			b := h[0]
			h = h[1:]
			v |= uint64(b&0x7f) << (7 * i)
			if b&0x80 == 0 {
				return v
			}
		}
		err = errNotRar
		return 0
	}
	vint() // header size
	typ, flags := vint(), vint()
	if typ == 4 {
		return false, true, nil // an encryption header first: the headers are encrypted
	}
	if typ != 1 {
		return false, false, errNotRar
	}
	if flags&1 != 0 {
		vint() // extra area size
	}
	if flags&2 != 0 {
		vint() // data size
	}
	arc := vint()
	return arc&1 != 0, false, err
}

// readRar checks what the front of a RAR says: not a volume, not encrypted,
// and a first file DMS² can decompress. The rest is only known as it is
// extracted, so its one Entry is the whole archive and nothing is renamed or
// taken as the Root.
func readRar(r io.ReaderAt, size int64, ver int) (*Archive, error) {
	front := make([]byte, min(HeadLen, size))
	n, err := r.ReadAt(front, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	front = front[:n]
	volume, encrypted, err := mainFlags(front, ver)
	switch {
	case err != nil:
		return nil, err
	case encrypted:
		return nil, errPassword
	case volume:
		return nil, errSplit
	}
	rr, err := rardecode.NewReader(bytes.NewReader(front), rardecode.MaxDictionarySize(rarMaxDict))
	if err != nil {
		return nil, errNotRar
	}
	h, err := rr.Next()
	whole := int64(len(front)) == size
	switch {
	case errors.Is(err, rardecode.ErrArchiveEncrypted), err == nil && h.Encrypted:
		return nil, errPassword
	case errors.Is(err, rardecode.ErrDictionaryTooLarge):
		return nil, errRarDict
	case err != nil && whole && !errors.Is(err, io.EOF):
		return nil, errNotRar
	}
	// Bytes the decompressor holds back while they wait to be freed: all of
	// a small archive, which is extracted once it is all there.
	margin := size
	if size > LargeEntry {
		margin = 2 * FreeStep
	}
	e := Entry{Name: "the archive", Stored: "the archive", Method: methodRar, CompressedSize: size, Size: size, Start: 0, End: size}
	// Total is a guess: the files' sizes are only known as they come.
	return &Archive{Entries: []Entry{e}, DirStart: size, Total: size, LargestCompressed: margin, rar: true}, nil
}

// rarState is what a RAR's extraction has found so far: its files are only
// known as they come.
type rarState struct {
	files atomic.Int64
	seen  map[string]bool      // folded names of the files extracted
	dirs  map[string]time.Time // folders made, by path, with their times
}

func newRarExtractor(x *Extractor, done []int64) (*Extractor, error) {
	s := &blockExtract{gate: newGate(x.f), prepared: true, rar: &rarState{seen: map[string]bool{}, dirs: map[string]time.Time{}}}
	s.decode = x.decodeRar
	x.z = s
	for _, st := range done {
		if st == x.a.Entries[0].Start {
			x.done[0] = true
		}
	}
	x.skipDone()
	return x, nil
}

// extractRar is Extract for a RAR: its one Entry once it is all on disk, or,
// large, as its bytes arrive.
func (x *Extractor) extractRar(ctx context.Context, missing []Range) (Result, error) {
	var r Result
	defer x.skipDone()
	if x.done[0] {
		return r, nil
	}
	e := x.a.Entries[0]
	var bad string
	var err error
	if large(e) {
		var finished bool
		bad, finished, err = x.blockStep(0, missing, &r)
		if err != nil || r.More || !finished && bad == "" {
			return r, err
		}
	} else {
		if !onDisk(missing, e.Start, e.End) {
			return r, nil
		}
		if bad, err = x.decodeRar(ctx, 0); err != nil {
			return r, err
		}
	}
	if bad != "" {
		r.Failed = append(r.Failed, &Failure{Entry: e, Range: Range{e.Start, e.End}, Reason: bad})
		return r, nil
	}
	x.done[0] = true
	if err := x.free(0); err != nil {
		x.done[0] = false
		return r, err
	}
	r.Done = append(r.Done, e)
	r.Freed = append(r.Freed, Range{e.Start, e.End})
	return r, nil
}

// decodeRar decompresses the whole RAR into the output folder, each file
// checked by the decoder and put in place. bad is why its bytes are wrong.
func (x *Extractor) decodeRar(ctx context.Context, _ int) (bad string, err error) {
	s := x.z.rar
	rr, err := rardecode.NewReader(io.NewSectionReader(x.z.gate, 0, x.a.Entries[0].End), rardecode.MaxDictionarySize(rarMaxDict))
	if err != nil {
		return rarBad(err)
	}
	for {
		h, err := rr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return rarBad(err)
		}
		if h.Encrypted {
			return "", errPassword
		}
		name, _, err := cleanName(h.Name)
		if err != nil || name == "" {
			if err == nil {
				err = errNoName
			}
			return "", err // a name that can't be made safe: the extraction stops
		}
		rel := name
		if h.IsDir {
			path := filepath.Join(x.dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(path, 0o755); err != nil {
				return "", err
			}
			s.dirs[path] = h.ModificationTime
			continue
		}
		if h.Mode()&os.ModeSymlink != 0 || h.LinkType != 0 {
			log.Printf("archive: skipped the link %s", name)
			continue
		}
		key := strings.ToLower(rel)
		if strings.HasSuffix(key, PartSuffix) {
			return "", unsupported("A file in it is named like the files DMS² hasn't finished extracting (%s)", name)
		}
		path := filepath.Join(x.dir, filepath.FromSlash(rel))
		out, err := create(path)
		if err != nil {
			return "", err
		}
		e := Entry{Name: name, Size: h.UnPackedSize, noCRC: true, Modified: h.ModificationTime, ReadOnly: h.Mode().Perm()&0o200 == 0}
		if h.UnKnownSize {
			e.Size = 1<<63 - 2
		}
		bad, err := copyEntry(ctx, e, rr, out)
		if h.UnKnownSize && strings.HasPrefix(bad, "it came out") {
			bad = "" // no size to check it by: the decoder's checksum did
		}
		if bad != "" || err != nil {
			discard(out)
			if err != nil {
				if b, e2 := rarBad(err); b != "" {
					return b, e2
				}
			}
			return bad, err
		}
		if err := finish(e, out, path, x.Mark); err != nil {
			return "", err
		}
		if !s.seen[key] {
			s.seen[key] = true
			s.files.Add(1)
		}
	}
	for path, t := range s.dirs {
		if !t.IsZero() {
			os.Chtimes(path, t, t)
		}
	}
	return "", nil
}

// rarBad sorts a decoder error: bytes that are wrong (fetched again), the
// file being read failing, or an archive DMS² can't extract.
func rarBad(err error) (bad string, _ error) {
	switch {
	case fileError(err):
		return "", err
	case errors.Is(err, rardecode.ErrArchiveEncrypted), errors.Is(err, rardecode.ErrArchivedFileEncrypted):
		return "", errPassword
	case errors.Is(err, rardecode.ErrDictionaryTooLarge):
		return "", errRarDict
	case errors.Is(err, rardecode.ErrMultiVolume):
		return "", errSplit
	case errors.Is(err, rardecode.ErrBadFileChecksum):
		return "its checksum doesn't match", nil
	}
	return "its data can't be decompressed", nil
}
