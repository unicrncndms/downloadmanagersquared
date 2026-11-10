// Package archive reads a ZIP archive's central directory, or a 7z's list of
// files, for Download as folder: where each Entry's bytes are, how big the
// extracted folder will be, and whether DMS² can extract the archive at all.
//
// It reads only the archive's tail, so it works on a download whose other
// bytes haven't arrived yet: Need says how much of the tail that is, and Read
// reads nothing before it.
package archive

import (
	"bufio"
	"cmp"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// TailGuess is how much of the archive's end to fetch before anything is
// known about it. Need then says exactly how much is needed.
const TailGuess = 64 << 10

// Compression methods DMS² can extract.
const (
	Store     uint16 = 0
	Deflate   uint16 = 8
	Deflate64 uint16 = 9  // Windows Explorer's, for large files
	Zstd      uint16 = 93 // Zstandard
	zstdOld   uint16 = 20 // Zstandard's number in APPNOTE 6.3.7, since deprecated
)

// extractable: DMS² can decompress the method.
func extractable(m uint16) bool {
	switch m {
	case Store, Deflate, Deflate64, Zstd, zstdOld:
		return true
	}
	return false
}

const (
	sigLocal    = 0x04034b50
	sigCentral  = 0x02014b50
	sigEnd      = 0x06054b50
	sigEnd64    = 0x06064b50
	sigLocator  = 0x07064b50
	endLen      = 22
	end64Len    = 56
	locatorLen  = 20
	centralLen  = 46
	localLen    = 30
	maxComment  = 0xffff
	endSearch   = endLen + maxComment // the end record can be this far from the end
	extraZip64  = 0x0001
	extraNTFS   = 0x000a
	extraUnixTS = 0x5455

	flagEncrypted = 1 << 0
	flagUTF8      = 1 << 11
)

// Entry is one file or folder stored in the archive.
type Entry struct {
	Name   string // where it goes inside the output folder, '/'-separated and safe for Windows
	Stored string // the name as the archive has it
	Dir    bool

	Method         uint16
	CRC32          uint32
	CompressedSize int64
	Size           int64

	// Start is where its local header is in the archive. Its bytes, from
	// that header to the end of any data descriptor, run up to End: the next
	// Entry's header, or the central directory.
	Start, End int64

	Modified time.Time
	ReadOnly bool

	noCRC bool // a 7z file the archive gives no CRC32 for
}

// Rename is an Entry name changed so Windows can use it.
type Rename struct{ From, To string }

// Archive is what the central directory says.
type Archive struct {
	Entries  []Entry // in the order their bytes come in the archive
	DirStart int64   // where the central directory starts; the archive's tail from here is what Need asks for
	Renamed  []Rename

	// Root is the archive's one top-level folder when every Entry sits
	// under it, so it can be the output folder itself; otherwise "".
	Root string

	Total             int64 // uncompressed size of all Entries
	LargestCompressed int64 // the largest Entry's compressed size

	z   *sevenZip // a 7z: its Entries are its blocks, and this has its files
	rar bool      // a RAR: its one Entry is the whole archive
}

// Unsupported is an archive that can't be downloaded as a folder, with the
// reason in words for the user.
type Unsupported struct{ Reason string }

func (u *Unsupported) Error() string { return u.Reason }

func unsupported(format string, a ...any) error {
	return &Unsupported{Reason: fmt.Sprintf(format, a...)}
}

var errNotZip = &Unsupported{Reason: "It isn't a ZIP file, or its list of files can't be read"}

// Need is how many bytes from the end of the archive Read needs, given that
// the last have bytes of the size-byte file are there, and its first HeadLen.
// Only those are read.
// When the answer is more than have, fetch that much and ask again: the first
// answer can be a guess, the one that fits in have is exact.
func Need(r io.ReaderAt, size, have int64) (int64, error) {
	have = min(have, size)
	if is7z(r) {
		return need7z(r, size, have)
	}
	if rarVersion(r) != 0 {
		return have, nil // nothing at its end: its front says it all
	}
	t, err := findTail(r, size, have)
	if err != nil {
		return 0, err
	}
	if t.more > 0 {
		return t.more, nil
	}
	return size - t.dirStart, nil
}

// Read reads the central directory and checks that the archive can be
// downloaded as a folder. It reads only the tail Need asks for, except that
// when an archive comment hides the end record it searches up to twice as far
// back as it has looked so far. An archive that can't be downloaded as a
// folder is an *Unsupported error.
func Read(r io.ReaderAt, size int64) (*Archive, error) {
	if is7z(r) {
		return read7z(r, size)
	}
	if v := rarVersion(r); v != 0 {
		return readRar(r, size, v)
	}
	// Start from the shortest possible end record and widen to what the
	// end records say, so nothing before the central directory is read.
	var t tail
	for have := int64(endLen); ; {
		var err error
		if t, err = findTail(r, size, have); err != nil {
			return nil, err
		}
		if t.more == 0 {
			break
		}
		have = t.more
	}
	if t.disks {
		return nil, errSplit
	}
	// Every central directory header is at least centralLen bytes, which
	// bounds the count before anything is allocated for it.
	if t.count > uint64(t.dirSize)/centralLen {
		return nil, errNotZip
	}

	br := bufio.NewReaderSize(io.NewSectionReader(r, t.dirStart, t.dirSize), 64<<10)
	entries := make([]Entry, 0, t.count)
	for range t.count {
		e, err := readCentral(br, t.base)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}

	slices.SortStableFunc(entries, func(a, b Entry) int { return cmp.Compare(a.Start, b.Start) })
	for i := range entries {
		end := t.dirStart
		if i+1 < len(entries) {
			end = entries[i+1].Start
		}
		e := &entries[i]
		// The local header and the data have to fit before the next thing.
		if e.Start < 0 || end-e.Start < localLen || end-e.Start-localLen < e.CompressedSize {
			return nil, errNotZip
		}
		e.End = end
	}

	a := &Archive{Entries: entries, DirStart: t.dirStart}
	if err := a.check(); err != nil {
		return nil, err
	}
	return a, nil
}

var errSplit = &Unsupported{Reason: "It's one part of a split archive"}

type tail struct {
	more     int64 // > 0: read this much from the end and ask again
	dirStart int64 // where the central directory really is
	dirSize  int64
	base     int64 // added to the archive's own offsets: bytes before the archive
	count    uint64
	disks    bool // the archive says it spans several disks
}

// findTail finds the end records and the central directory, reading only the
// last have bytes.
func findTail(r io.ReaderAt, size, have int64) (tail, error) {
	if size < endLen {
		return tail{}, errNotZip
	}
	window := min(have, endSearch)
	buf := make([]byte, window)
	if _, err := r.ReadAt(buf, size-window); err != nil && err != io.EOF {
		return tail{}, err
	}
	at := -1
	for i := len(buf) - endLen; i >= 0; i-- {
		if binary.LittleEndian.Uint32(buf[i:]) == sigEnd {
			// The comment must end within the file.
			if n := int(binary.LittleEndian.Uint16(buf[i+20:])); i+endLen+n <= len(buf) {
				at = i
				break
			}
		}
	}
	if at < 0 {
		if window < min(size, endSearch) {
			// A comment hides the end record: widen the search.
			return tail{more: min(size, endSearch+locatorLen, max(2*window, 1<<10))}, nil
		}
		return tail{}, errNotZip
	}
	endAt := size - window + int64(at)
	e := buf[at:]
	disk, dirDisk := binary.LittleEndian.Uint16(e[4:]), binary.LittleEndian.Uint16(e[6:])
	onDisk, count := binary.LittleEndian.Uint16(e[8:]), binary.LittleEndian.Uint16(e[10:])
	t := tail{
		dirSize: int64(binary.LittleEndian.Uint32(e[12:])),
		count:   uint64(count),
		disks:   disk != 0 || dirDisk != 0 || onDisk != count,
	}
	dirOffset := int64(binary.LittleEndian.Uint32(e[16:]))
	dirEnd := endAt

	// A ZIP64 locator sits right before the end record.
	if endAt >= locatorLen {
		if size-(endAt-locatorLen) > have {
			return tail{more: size - (endAt - locatorLen)}, nil
		}
		var l [locatorLen]byte
		if _, err := r.ReadAt(l[:], endAt-locatorLen); err != nil {
			return tail{}, err
		}
		if binary.LittleEndian.Uint32(l[:]) == sigLocator {
			if binary.LittleEndian.Uint32(l[4:]) != 0 || binary.LittleEndian.Uint32(l[16:]) > 1 {
				t.disks = true
			}
			recAt, more, err := findEnd64(r, size, have, int64(binary.LittleEndian.Uint64(l[8:])), endAt-locatorLen)
			if err != nil {
				return tail{}, err
			}
			if more > 0 {
				return tail{more: more}, nil
			}
			var z [end64Len]byte
			if _, err := r.ReadAt(z[:], recAt); err != nil {
				return tail{}, err
			}
			if binary.LittleEndian.Uint32(z[16:]) != 0 || binary.LittleEndian.Uint32(z[20:]) != 0 ||
				binary.LittleEndian.Uint64(z[24:]) != binary.LittleEndian.Uint64(z[32:]) {
				t.disks = true
			}
			t.count = binary.LittleEndian.Uint64(z[32:])
			dirSize, dirOff := binary.LittleEndian.Uint64(z[40:]), binary.LittleEndian.Uint64(z[48:])
			if dirSize > math.MaxInt64 || dirOff > math.MaxInt64 {
				return tail{}, errNotZip
			}
			t.dirSize, dirOffset, dirEnd = int64(dirSize), int64(dirOff), recAt
		}
	}

	// The central directory ends where the end records start. Where the
	// archive says it is tells how many bytes come before the archive.
	t.dirStart = dirEnd - t.dirSize
	t.base = t.dirStart - dirOffset
	if t.dirStart < 0 || t.base < 0 {
		return tail{}, errNotZip
	}
	if size-t.dirStart > have {
		return tail{more: size - t.dirStart}, nil
	}
	return t, nil
}

// findEnd64 finds the ZIP64 end record. It is usually right before the
// locator; otherwise it is where the locator says (a record with extra data
// is longer). more > 0: it lies before the have bytes, read that much from
// the end and ask again.
func findEnd64(r io.ReaderAt, size, have, said, locatorAt int64) (at, more int64, err error) {
	for _, at := range []int64{locatorAt - end64Len, said} {
		if at < 0 || at+end64Len > locatorAt {
			continue
		}
		if size-at > have {
			return 0, size - at, nil
		}
		var sig [4]byte
		if _, err := r.ReadAt(sig[:], at); err != nil {
			return 0, 0, err
		}
		if binary.LittleEndian.Uint32(sig[:]) == sigEnd64 {
			return at, 0, nil
		}
	}
	return 0, 0, errNotZip
}

// readCentral reads one central directory header.
func readCentral(br *bufio.Reader, base int64) (Entry, error) {
	var h [centralLen]byte
	if _, err := io.ReadFull(br, h[:]); err != nil {
		return Entry{}, errNotZip
	}
	le := binary.LittleEndian
	if le.Uint32(h[:]) != sigCentral {
		return Entry{}, errNotZip
	}
	madeBy, flags, method := le.Uint16(h[4:]), le.Uint16(h[8:]), le.Uint16(h[10:])
	dosTime, dosDate := le.Uint16(h[12:]), le.Uint16(h[14:])
	crc := le.Uint32(h[16:])
	csize, usize := uint64(le.Uint32(h[20:])), uint64(le.Uint32(h[24:]))
	nameLen, extraLen, commentLen := int(le.Uint16(h[28:])), int(le.Uint16(h[30:])), int(le.Uint16(h[32:]))
	disk := uint32(le.Uint16(h[34:]))
	external := le.Uint32(h[38:])
	offset := uint64(le.Uint32(h[42:]))

	rest := make([]byte, nameLen+extraLen+commentLen)
	if _, err := io.ReadFull(br, rest); err != nil {
		return Entry{}, errNotZip
	}
	rawName, extra := rest[:nameLen], rest[nameLen:nameLen+extraLen]

	var modified time.Time
	for len(extra) >= 4 {
		id, n := le.Uint16(extra), int(le.Uint16(extra[2:]))
		if 4+n > len(extra) {
			break
		}
		f := extra[4 : 4+n]
		extra = extra[4+n:]
		switch id {
		case extraZip64:
			// Only the fields that didn't fit are here, in this order.
			next := func(v *uint64) {
				if *v == math.MaxUint32 && len(f) >= 8 {
					*v, f = le.Uint64(f), f[8:]
				}
			}
			next(&usize)
			next(&csize)
			next(&offset)
			if disk == math.MaxUint16 && len(f) >= 4 {
				disk = le.Uint32(f)
			}
		case extraUnixTS:
			if len(f) >= 5 && f[0]&1 != 0 {
				modified = time.Unix(int64(int32(le.Uint32(f[1:]))), 0).UTC()
			}
		case extraNTFS:
			if t, ok := ntfsTime(f); ok && modified.IsZero() {
				modified = t
			}
		}
	}
	if modified.IsZero() {
		modified = dosToTime(dosDate, dosTime)
	}
	if csize > math.MaxInt64 || usize > math.MaxInt64 || offset > math.MaxInt64-uint64(base) {
		return Entry{}, errNotZip
	}

	stored := decodeName(rawName, flags&flagUTF8 != 0)
	e := Entry{
		Stored:         stored,
		Dir:            strings.HasSuffix(stored, "/") || strings.HasSuffix(stored, `\`) || isDir(madeBy, external),
		Method:         method,
		CRC32:          crc,
		CompressedSize: int64(csize),
		Size:           int64(usize),
		Start:          base + int64(offset),
		Modified:       modified,
	}

	switch {
	case disk != 0:
		return Entry{}, errSplit
	case flags&flagEncrypted != 0 || method == 99:
		return Entry{}, unsupported("It contains encrypted files (%s)", stored)
	case isLink(madeBy, external):
		return Entry{}, unsupported("It contains a symbolic link, which DMS² can't extract (%s)", stored)
	case !extractable(method):
		return Entry{}, unsupported("It uses a compression method DMS² can't extract: %s (%s)", methodName(method), stored)
	case method == Store && csize != usize:
		return Entry{}, errNotZip // stored data is the file itself
	}
	e.ReadOnly = readOnly(madeBy, external)
	return e, nil
}

const (
	creatorUnix = 3
	creatorMac  = 19
)

func unixMode(madeBy uint16, external uint32) (uint32, bool) {
	switch madeBy >> 8 {
	case creatorUnix, creatorMac:
		return external >> 16, external>>16 != 0
	}
	return 0, false
}

func isLink(madeBy uint16, external uint32) bool {
	if mode, ok := unixMode(madeBy, external); ok && mode&0o170000 == 0o120000 {
		return true
	}
	return external&0x400 != 0 // FILE_ATTRIBUTE_REPARSE_POINT
}

func isDir(madeBy uint16, external uint32) bool {
	if mode, ok := unixMode(madeBy, external); ok {
		return mode&0o170000 == 0o040000
	}
	return external&0x10 != 0 // FILE_ATTRIBUTE_DIRECTORY
}

func readOnly(madeBy uint16, external uint32) bool {
	if mode, ok := unixMode(madeBy, external); ok {
		return mode&0o200 == 0
	}
	return external&1 != 0 // FILE_ATTRIBUTE_READONLY
}

func methodName(m uint16) string {
	switch m {
	case 9:
		return "Deflate64"
	case 12:
		return "BZIP2"
	case 14:
		return "LZMA"
	case 20, 93:
		return "Zstandard"
	case 95:
		return "XZ"
	case 98:
		return "PPMd"
	}
	return fmt.Sprintf("method %d", m)
}

// ntfsTime reads the modification time from an NTFS extra field.
func ntfsTime(f []byte) (time.Time, bool) {
	if len(f) < 4 {
		return time.Time{}, false
	}
	f = f[4:] // reserved
	for len(f) >= 4 {
		tag, n := binary.LittleEndian.Uint16(f), int(binary.LittleEndian.Uint16(f[2:]))
		if 4+n > len(f) {
			break
		}
		if tag == 1 && n >= 8 {
			// 100 ns ticks since 1601.
			ticks := binary.LittleEndian.Uint64(f[4:])
			const secsTo1970 = 11644473600
			return time.Unix(int64(ticks/1e7)-secsTo1970, int64(ticks%1e7)*100).UTC(), true
		}
		f = f[4+n:]
	}
	return time.Time{}, false
}

// dosToTime: MS-DOS dates are local time, to two seconds.
func dosToTime(d, t uint16) time.Time {
	return time.Date(int(d>>9)+1980, time.Month(d>>5&0xf), int(d&0x1f),
		int(t>>11), int(t>>5&0x3f), int(t&0x1f)*2, 0, time.Local)
}

// decodeName: names not marked UTF-8 are code page 437, unless they are
// valid UTF-8 anyway, as many writers don't set the flag.
func decodeName(b []byte, utf bool) string {
	if utf || utf8.Valid(b) {
		return string(b)
	}
	s, err := charmap.CodePage437.NewDecoder().Bytes(b)
	if err != nil {
		return string(b)
	}
	return string(s)
}

var errNoName = &Unsupported{Reason: "A file in it has no name"}

// check cleans the names and finds the root, turning down archives whose
// names can't be made safe.
func (a *Archive) check() error {
	files := map[string]string{} // folded cleaned name -> cleaned name
	dirs := map[string]bool{}
	for i := range a.Entries {
		e := &a.Entries[i]
		name, changed, err := cleanName(e.Stored)
		if err != nil {
			return err
		}
		if name == "" {
			if e.Dir {
				continue // "./" and the like: the output folder itself
			}
			return errNoName
		}
		key := strings.ToLower(name)
		if e.Dir {
			e.Name = name + "/"
			dirs[key] = true
		} else {
			e.Name = name
			if _, dup := files[key]; dup {
				return unsupported("Two files in it have the same name (%s)", name)
			}
			files[key] = name
		}
		for p := key; ; {
			j := strings.LastIndexByte(p, '/')
			if j < 0 {
				break
			}
			p = p[:j]
			dirs[p] = true
		}
		if changed {
			a.Renamed = append(a.Renamed, Rename{From: e.Stored, To: e.Name})
		}
		a.Total += e.Size
		a.LargestCompressed = max(a.LargestCompressed, e.CompressedSize)
	}
	for key, name := range files {
		if dirs[key] {
			return unsupported("Two files in it have the same name (%s)", name)
		}
	}
	a.Root = root(a.Entries)
	return nil
}

// root is the one top-level folder every Entry is in, if there is one and
// something is inside it.
func root(entries []Entry) string {
	top, inside := "", false
	for _, e := range entries {
		if e.Name == "" {
			continue
		}
		first, rest, _ := strings.Cut(e.Name, "/")
		if top == "" {
			top = first
		} else if !strings.EqualFold(first, top) {
			return ""
		}
		if !strings.Contains(e.Name, "/") {
			return "" // a file at the top
		}
		if rest != "" {
			inside = true
		}
	}
	if !inside {
		return ""
	}
	return top
}

// cleanName turns a stored name into a '/'-separated path inside the output
// folder that Windows can use, without a trailing slash. changed says it had
// to be renamed for Windows (not just its separators or "." folders tidied).
func cleanName(stored string) (name string, changed bool, err error) {
	if stored == "" {
		return "", false, errNoName
	}
	s := strings.ReplaceAll(stored, `\`, "/")
	outside := unsupported("A file in it would be saved outside its folder (%s)", stored)
	if strings.HasPrefix(s, "/") || len(s) >= 2 && s[1] == ':' && isLetter(s[0]) {
		return "", false, outside
	}
	var parts []string
	for _, p := range strings.Split(s, "/") {
		switch p {
		case "", ".":
			continue
		case "..":
			return "", false, outside
		}
		c := CleanPart(p)
		if c != p {
			changed = true
		}
		parts = append(parts, c)
	}
	return strings.Join(parts, "/"), changed, nil
}

func isLetter(b byte) bool { return 'a' <= b|0x20 && b|0x20 <= 'z' }

// CleanPart makes one path element safe for Windows.
func CleanPart(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r < 32 || strings.ContainsRune(`<>:"|?*`, r) {
			b.WriteByte('_')
		} else {
			b.WriteRune(r)
		}
	}
	s := strings.TrimRight(b.String(), ". ")
	if s == "" {
		return "_"
	}
	base, _, _ := strings.Cut(s, ".")
	if reserved(strings.TrimRight(base, " ")) {
		s = "_" + s
	}
	return s
}

func reserved(base string) bool {
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
		"COM¹", "COM²", "COM³", "LPT¹", "LPT²", "LPT³":
		return true
	}
	return false
}
