package archive

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/bodgit/sevenzip"
)

// Extractable: a file by this name may be downloaded as a folder: a .zip, .7z or .rar.
func Extractable(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".zip", ".7z", ".rar":
		return true
	}
	return false
}

// HeadLen is how much of the archive's front Need and Read read, besides its
// tail: a 7z's start header, which says where its list of files is, or a
// RAR's first headers.
const HeadLen = 64 << 10

// sevenStartLen is a 7z's start header.
const sevenStartLen = 32

var sevenSig = []byte{'7', 'z', 0xbc, 0xaf, 0x27, 0x1c}

// is7z: the archive starts with a 7z signature. Its first bytes must be on disk.
func is7z(r io.ReaderAt) bool {
	b := make([]byte, len(sevenSig))
	_, err := r.ReadAt(b, 0)
	return err == nil && bytes.Equal(b, sevenSig)
}

// method7z marks an Entry that is a 7z block: a run of packed bytes that
// decompresses into one or more files, in order.
const method7z uint16 = 0xffff

// sevenZip is what a 7z archive's list of files says, beyond its Entries.
type sevenZip struct {
	files  []sevenFile
	blocks [][]int // per Entry: its files (indexes into files), in the order they decompress
	multi  []bool  // per Entry: its block reads several packed streams at once, so it can't be extracted as its bytes arrive
}

type sevenFile struct {
	Entry     // Name, Stored, Dir, Size, CRC32, Modified, ReadOnly
	index int // in sevenzip.Reader.File
	block int // its Entry, -1 for a folder or an empty file
}

// needFrom is a read of bytes before the tail that is on disk.
type needFrom struct{ at int64 }

func (n *needFrom) Error() string { return "the archive's list of files needs more of its tail" }

// headTail serves the sevenzip reader a 7z's start header and the tail from
// `from` on. Reading anything else fails with needFrom. low is where the
// lowest read in the tail started.
type headTail struct {
	r          io.ReaderAt
	size, from int64
	low        int64
}

func (h *headTail) ReadAt(p []byte, off int64) (int, error) {
	if off == 0 && len(p) > sevenStartLen {
		// The reader's search for the signature: only the start header counts.
		n := min(int64(len(p)), h.size)
		clear(p)
		if _, err := h.r.ReadAt(p[:min(sevenStartLen, n)], 0); err != nil {
			return 0, err
		}
		if n < int64(len(p)) {
			return int(n), io.EOF
		}
		return len(p), nil
	}
	if off+int64(len(p)) <= sevenStartLen {
		return h.r.ReadAt(p, off)
	}
	if off < h.from {
		return 0, &needFrom{off}
	}
	h.low = min(h.low, off)
	return h.r.ReadAt(p, off)
}

var errNot7z = &Unsupported{Reason: "It isn't a 7z file, or its list of files can't be read"}

// open7z reads a 7z's list of files from its tail from `from` on. A needFrom
// error says where the tail it needs starts.
func open7z(r io.ReaderAt, size, from int64) (*sevenzip.Reader, int64, error) {
	ht := &headTail{r: r, size: size, from: from, low: size}
	zr, err := sevenzip.NewReader(ht, size)
	var nf *needFrom
	switch {
	case errors.As(err, &nf):
		return nil, 0, nf
	case encrypted(err):
		return nil, 0, errPassword
	case err != nil:
		return nil, 0, errNot7z
	}
	return zr, ht.low, nil
}

var errPassword = &Unsupported{Reason: "It's protected with a password"}

func encrypted(err error) bool {
	var re *sevenzip.ReadError
	return errors.As(err, &re) && re.Encrypted
}

func need7z(r io.ReaderAt, size, have int64) (int64, error) {
	_, low, err := open7z(r, size, size-have)
	var nf *needFrom
	if errors.As(err, &nf) {
		return size - nf.at, nil
	}
	if err != nil {
		return 0, err
	}
	return size - low, nil
}

// read7z reads a 7z's list of files, widening the tail it reads as needed.
func read7z(r io.ReaderAt, size int64) (*Archive, error) {
	from := size
	for range 16 {
		zr, low, err := open7z(r, size, from)
		var nf *needFrom
		if errors.As(err, &nf) {
			from = nf.at
			continue
		}
		if err != nil {
			return nil, err
		}
		return build7z(zr, low)
	}
	return nil, errNot7z
}

// aesID is the 7z coder that encrypts.
var aesID = []byte{0x06, 0xf1, 0x07, 0x01}

// build7z turns the list into Entries, one per block, with where each
// block's bytes are: the sevenzip package keeps that to itself, so it is read
// from its fields (TestSevenZipNeedAndReadUseOnlyTheHeadAndTail notices if they change).
func build7z(zr *sevenzip.Reader, dirStart int64) (*Archive, error) {
	v := reflect.ValueOf(zr).Elem()
	start := v.FieldByName("start").Int()
	si := v.FieldByName("si")
	var blocks []Entry
	var multi []bool
	if !si.IsNil() && !si.Elem().FieldByName("packInfo").IsNil() && !si.Elem().FieldByName("unpackInfo").IsNil() {
		pi := si.Elem().FieldByName("packInfo").Elem()
		at := start + int64(pi.FieldByName("position").Uint())
		sizes := pi.FieldByName("size")
		folders := si.Elem().FieldByName("unpackInfo").Elem().FieldByName("folder")
		k := 0
		for i := range folders.Len() {
			f := folders.Index(i).Elem()
			n := int(f.FieldByName("packedStreams").Uint())
			coders := f.FieldByName("coder")
			for j := range coders.Len() {
				if bytes.Equal(coders.Index(j).Elem().FieldByName("id").Bytes(), aesID) {
					return nil, errPassword
				}
			}
			e := Entry{Method: method7z, Start: at}
			for range n {
				if k >= sizes.Len() {
					return nil, errNot7z
				}
				e.CompressedSize += int64(sizes.Index(k).Uint())
				k++
			}
			at += e.CompressedSize
			e.End = at
			blocks = append(blocks, e)
			multi = append(multi, n > 1)
		}
	}
	if len(blocks) > 0 && blocks[len(blocks)-1].End > dirStart {
		return nil, errNot7z
	}

	z := &sevenZip{blocks: make([][]int, len(blocks)), multi: multi}
	var files []Entry // for check
	for i, f := range zr.File {
		mode := f.FileInfo().Mode()
		if mode&fs.ModeSymlink != 0 {
			return nil, unsupported("It contains a link (%s), which DMS² doesn't extract", f.Name)
		}
		empty := reflect.ValueOf(f).Elem().FieldByName("FileHeader").FieldByName("isEmptyStream").Bool()
		sf := sevenFile{index: i, block: -1}
		sf.Stored, sf.Dir, sf.Size = f.Name, mode.IsDir(), int64(f.UncompressedSize)
		sf.CRC32, sf.noCRC, sf.Modified, sf.ReadOnly = f.CRC32, f.CRC32 == 0, f.Modified, f.Attributes&1 != 0
		if !empty && !sf.Dir {
			if f.Stream < 0 || f.Stream >= len(blocks) {
				return nil, errNot7z
			}
			sf.block = f.Stream
			z.blocks[f.Stream] = append(z.blocks[f.Stream], len(z.files))
			blocks[f.Stream].Size += sf.Size
		}
		z.files = append(z.files, sf)
		files = append(files, sf.Entry)
	}

	// The names are checked, cleaned and the Root found as for a ZIP.
	named := &Archive{Entries: files}
	if err := named.check(); err != nil {
		return nil, err
	}
	for i := range z.files {
		z.files[i].Entry = named.Entries[i]
	}
	a := &Archive{DirStart: dirStart, Renamed: named.Renamed, Root: named.Root, Total: named.Total, z: z}
	for i, e := range blocks {
		fs := z.blocks[i]
		if len(fs) == 0 {
			continue // nothing in it: never needed
		}
		e.Name = z.files[fs[0]].Name
		if len(fs) > 1 {
			e.Name = fmt.Sprintf("%s and %d other files packed with it", e.Name, len(fs)-1)
		}
		e.Stored = e.Name
		a.Entries = append(a.Entries, e)
		a.LargestCompressed = max(a.LargestCompressed, e.CompressedSize)
	}
	// Entries only for blocks with files: renumber the files' blocks to them.
	idx := make([]int, len(blocks))
	keep := 0
	var kept [][]int
	var keptMulti []bool
	for i := range blocks {
		idx[i] = -1
		if len(z.blocks[i]) > 0 {
			idx[i] = keep
			keep++
			kept = append(kept, z.blocks[i])
			keptMulti = append(keptMulti, z.multi[i])
		}
	}
	for i := range z.files {
		if b := z.files[i].block; b >= 0 {
			z.files[i].block = idx[b]
		}
	}
	z.blocks, z.multi = kept, keptMulti
	return a, nil
}
