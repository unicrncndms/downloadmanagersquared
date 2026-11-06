package archive

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/winutil"
)

// goZip builds an archive with Go's own writer, which uses data descriptors
// for every file it compresses.
type goFile struct {
	name     string
	data     []byte
	store    bool
	mode     fs.FileMode
	modified time.Time
}

func goZip(t *testing.T, comment string, files ...goFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: f.modified}
		if f.store {
			h.Method = zip.Store
		}
		if f.mode != 0 {
			h.SetMode(f.mode)
		}
		fw, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(f.data); err != nil {
			t.Fatal(err)
		}
	}
	if comment != "" {
		if err := w.SetComment(comment); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readBytes(t *testing.T, b []byte) *Archive {
	t.Helper()
	a, err := Read(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return a
}

func random(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(int64(n))).Read(b)
	return b
}

func names(a *Archive) []string {
	var n []string
	for _, e := range a.Entries {
		n = append(n, e.Name)
	}
	return n
}

// checkRanges: the Entries cover the archive from the first one to the
// central directory, back to back.
func checkRanges(t *testing.T, a *Archive) {
	t.Helper()
	for i, e := range a.Entries {
		if e.End <= e.Start {
			t.Fatalf("%s: empty range %d-%d", e.Name, e.Start, e.End)
		}
		next := a.DirStart
		if i+1 < len(a.Entries) {
			next = a.Entries[i+1].Start
		}
		if e.End != next {
			t.Fatalf("%s ends at %d, the next thing starts at %d", e.Name, e.End, next)
		}
	}
}

func TestReadsWhatGosWriterWrote(t *testing.T) {
	when := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	text := bytes.Repeat([]byte("hello, archive "), 1000)
	bin := random(5000)
	b := goZip(t, "",
		goFile{name: "notes.txt", data: text, modified: when},
		goFile{name: "sub/", modified: when},
		goFile{name: "sub/data.bin", data: bin, store: true, mode: 0o444, modified: when},
	)
	a := readBytes(t, b)

	if got, want := names(a), []string{"notes.txt", "sub/", "sub/data.bin"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("names %q, want %q", got, want)
	}
	checkRanges(t, a)
	if a.Entries[0].Start != 0 {
		t.Fatalf("first Entry starts at %d", a.Entries[0].Start)
	}

	notes, sub, data := a.Entries[0], a.Entries[1], a.Entries[2]
	if notes.Method != Deflate || notes.Size != int64(len(text)) || notes.CRC32 != crc32.ChecksumIEEE(text) || notes.Dir {
		t.Fatalf("notes.txt: %+v", notes)
	}
	if notes.CompressedSize >= notes.Size {
		t.Fatalf("notes.txt wasn't compressed: %d of %d", notes.CompressedSize, notes.Size)
	}
	if !sub.Dir || sub.Size != 0 {
		t.Fatalf("sub/: %+v", sub)
	}
	if data.Method != Store || data.Size != 5000 || data.CompressedSize != 5000 || data.CRC32 != crc32.ChecksumIEEE(bin) {
		t.Fatalf("sub/data.bin: %+v", data)
	}
	if !data.ReadOnly || notes.ReadOnly {
		t.Fatalf("read-only: data.bin %v, notes.txt %v", data.ReadOnly, notes.ReadOnly)
	}
	for _, e := range a.Entries {
		if !e.Modified.Equal(when) {
			t.Fatalf("%s modified %v, want %v", e.Name, e.Modified, when)
		}
	}
	if a.Total != int64(len(text)+len(bin)) {
		t.Fatalf("total %d", a.Total)
	}
	if a.LargestCompressed != 5000 {
		t.Fatalf("largest compressed %d, want 5000", a.LargestCompressed)
	}
	if a.Root != "" {
		t.Fatalf("root %q for a mixed archive", a.Root)
	}
}

func TestAnEntrysRangeIncludesItsDataDescriptor(t *testing.T) {
	b := goZip(t, "", goFile{name: "a.txt", data: []byte("aaaa")}, goFile{name: "b.txt", data: []byte("bbbb")})
	a := readBytes(t, b)
	for _, e := range a.Entries {
		// Go writes the descriptor's signature, CRC and both sizes: 16 bytes.
		if sig := binary.LittleEndian.Uint32(b[e.End-16:]); sig != 0x08074b50 {
			t.Fatalf("%s: no data descriptor at the end of its range (%#x)", e.Name, sig)
		}
	}
}

func TestDOSTimesAreUsedWithoutAnExtendedTimestamp(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: "old.txt", Method: zip.Store}
	// The DOS fields on their own, as an old writer leaves them.
	//lint:ignore SA1019 the deprecated fields are what this test is about
	h.ModifiedDate = uint16((2001-1980)<<9 | 2<<5 | 3)
	//lint:ignore SA1019 as above
	h.ModifiedTime = uint16(4<<11 | 5<<5 | 6/2)
	if _, err := w.CreateHeader(h); err != nil {
		t.Fatal(err)
	}
	w.Close()
	e := readBytes(t, buf.Bytes()).Entries[0]
	if want := time.Date(2001, 2, 3, 4, 5, 6, 0, time.Local); !e.Modified.Equal(want) {
		t.Fatalf("modified %v, want %v", e.Modified, want)
	}
}

// tailOnly fails any read before the last n bytes, to show what a function needs.
type tailOnly struct {
	b []byte
	n int64
}

func (r tailOnly) ReadAt(p []byte, off int64) (int, error) {
	if off < int64(len(r.b))-r.n {
		return 0, fmt.Errorf("read at %d, before the last %d bytes", off, r.n)
	}
	return bytes.NewReader(r.b).ReadAt(p, off)
}

func TestNeedGivesTheExactTailAndReadNeedsNoMore(t *testing.T) {
	b := goZip(t, "", goFile{name: "big.bin", data: random(300 << 10), store: true}, goFile{name: "x.txt", data: []byte("x")})
	size := int64(len(b))

	n, err := Need(tailOnly{b, TailGuess}, size, TailGuess)
	if err != nil {
		t.Fatal(err)
	}
	a := readBytes(t, b)
	if n != size-a.DirStart {
		t.Fatalf("need %d, want %d (from the central directory)", n, size-a.DirStart)
	}
	if _, err := Read(tailOnly{b, n}, size); err != nil {
		t.Fatalf("Read with only the tail Need asked for: %v", err)
	}
}

func TestNeedAsksForMoreWhenALongCommentHidesTheEnd(t *testing.T) {
	comment := strings.Repeat("c", 65535)
	b := goZip(t, comment, goFile{name: "big.bin", data: random(300 << 10), store: true})
	size := int64(len(b))

	n, err := Need(tailOnly{b, TailGuess}, size, TailGuess)
	if err != nil {
		t.Fatal(err)
	}
	if n <= TailGuess {
		t.Fatalf("need %d, want more than the first guess", n)
	}
	n2, err := Need(tailOnly{b, n}, size, n)
	if err != nil {
		t.Fatal(err)
	}
	if want := size - readBytes(t, b).DirStart; n2 != want {
		t.Fatalf("second need %d, want %d", n2, want)
	}
}

func TestNeedOnASmallFileAsksForAtMostTheFile(t *testing.T) {
	b := goZip(t, "", goFile{name: "a.txt", data: []byte("a")})
	n, err := Need(bytes.NewReader(b), int64(len(b)), TailGuess)
	if err != nil {
		t.Fatal(err)
	}
	if n > int64(len(b)) {
		t.Fatalf("need %d of a %d-byte file", n, len(b))
	}
}

func TestReadsMoreThan65535Entries(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 70,000 Entries")
	}
	const count = 70000
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for i := range count {
		fw, err := w.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("f/%05d", i), Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		fw.Write([]byte{byte(i)})
	}
	w.Close()
	a := readBytes(t, buf.Bytes())
	if len(a.Entries) != count {
		t.Fatalf("%d Entries, want %d", len(a.Entries), count)
	}
	if a.Entries[count-1].Name != fmt.Sprintf("f/%05d", count-1) {
		t.Fatalf("last Entry %q", a.Entries[count-1].Name)
	}
	if a.Root != "f" || a.Total != count {
		t.Fatalf("root %q, total %d", a.Root, a.Total)
	}
	checkRanges(t, a)
}

// raw writes an archive by hand, for what Go's writer won't make.
type rawEntry struct {
	name     string
	data     []byte
	hole     int64 // stored bytes left as a sparse hole instead of data
	method   uint16
	flags    uint16
	madeBy   uint16 // upper byte: the creator's system
	external uint32
	disk     uint16
	offset   int64 // -1 or 0: right after the previous one; else this offset (for overlaps)
}

type rawOpts struct {
	prefix   int64 // bytes before the archive, as in a self-extractor
	eocdDisk uint16
	zip64    bool
	end64Pad int // extensible data in the ZIP64 end record, making it longer
}

func le16(b []byte, v uint16) []byte { return binary.LittleEndian.AppendUint16(b, v) }
func le32(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }
func le64(b []byte, v uint64) []byte { return binary.LittleEndian.AppendUint64(b, v) }

func writeRaw(t *testing.T, w io.WriteSeeker, entries []rawEntry, o rawOpts) int64 {
	t.Helper()
	const max32 = 0xffffffff
	pos := o.prefix
	if _, err := w.Seek(pos, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	put := func(b []byte) {
		if _, err := w.Write(b); err != nil {
			t.Fatal(err)
		}
		pos += int64(len(b))
	}
	var cd []byte
	starts := make([]int64, len(entries))
	for i, e := range entries {
		if e.offset > 0 {
			pos = e.offset
			if _, err := w.Seek(pos, io.SeekStart); err != nil {
				t.Fatal(err)
			}
		}
		starts[i] = pos - o.prefix
		size := int64(len(e.data)) + e.hole
		crc := crc32.ChecksumIEEE(e.data)
		big := size >= max32
		size32 := uint32(size)
		if big {
			size32 = max32
		}
		var lextra []byte
		if big {
			lextra = le64(le64(le16(le16(nil, 1), 16), uint64(size)), uint64(size))
		}
		h := le32(nil, 0x04034b50)
		h = le16(h, 45)
		h = le16(h, e.flags)
		h = le16(h, e.method)
		h = le16(h, 0)
		h = le16(h, 0x21)
		h = le32(h, crc)
		h = le32(h, size32)
		h = le32(h, size32)
		h = le16(h, uint16(len(e.name)))
		h = le16(h, uint16(len(lextra)))
		put(append(append(h, e.name...), lextra...))
		put(e.data)
		if e.hole > 0 {
			pos += e.hole
			if _, err := w.Seek(pos, io.SeekStart); err != nil {
				t.Fatal(err)
			}
		}

		off := starts[i]
		off32 := uint32(off)
		var z []byte
		if big {
			z = le64(le64(z, uint64(size)), uint64(size))
		}
		if off >= max32 {
			z = le64(z, uint64(off))
			off32 = max32
		}
		var cextra []byte
		if len(z) > 0 {
			cextra = append(le16(le16(nil, 1), uint16(len(z))), z...)
		}
		c := le32(nil, 0x02014b50)
		c = le16(c, e.madeBy|45)
		c = le16(c, 45)
		c = le16(c, e.flags)
		c = le16(c, e.method)
		c = le16(c, 0)
		c = le16(c, 0x21)
		c = le32(c, crc)
		c = le32(c, size32)
		c = le32(c, size32)
		c = le16(c, uint16(len(e.name)))
		c = le16(c, uint16(len(cextra)))
		c = le16(c, 0)
		c = le16(c, e.disk)
		c = le16(c, 0)
		c = le32(c, e.external)
		c = le32(c, off32)
		cd = append(append(append(cd, c...), e.name...), cextra...)
	}
	cdStart := pos - o.prefix
	put(cd)
	if o.zip64 || cdStart >= max32 {
		recAt := pos - o.prefix
		r := le32(nil, 0x06064b50)
		r = le64(r, uint64(44+o.end64Pad))
		r = le16(r, 45)
		r = le16(r, 45)
		r = le32(r, uint32(o.eocdDisk))
		r = le32(r, uint32(o.eocdDisk))
		r = le64(r, uint64(len(entries)))
		r = le64(r, uint64(len(entries)))
		r = le64(r, uint64(len(cd)))
		r = le64(r, uint64(cdStart))
		put(append(r, make([]byte, o.end64Pad)...))
		l := le32(nil, 0x07064b50)
		l = le32(l, uint32(o.eocdDisk))
		l = le64(l, uint64(recAt))
		l = le32(l, uint32(o.eocdDisk)+1)
		put(l)
	}
	cdOff32 := uint32(cdStart)
	if cdStart >= max32 {
		cdOff32 = max32
	}
	e := le32(nil, 0x06054b50)
	e = le16(e, o.eocdDisk)
	e = le16(e, o.eocdDisk)
	e = le16(e, uint16(len(entries)))
	e = le16(e, uint16(len(entries)))
	e = le32(e, uint32(len(cd)))
	e = le32(e, cdOff32)
	e = le16(e, 0)
	put(e)
	return pos
}

type memFile struct {
	b   []byte
	pos int64
}

func (m *memFile) Write(p []byte) (int, error) {
	if need := m.pos + int64(len(p)); need > int64(len(m.b)) {
		m.b = append(m.b, make([]byte, need-int64(len(m.b)))...)
	}
	copy(m.b[m.pos:], p)
	m.pos += int64(len(p))
	return len(p), nil
}

func (m *memFile) Seek(off int64, whence int) (int64, error) {
	if whence != io.SeekStart {
		return 0, errors.New("only SeekStart")
	}
	m.pos = off
	return off, nil
}

func raw(t *testing.T, o rawOpts, entries ...rawEntry) []byte {
	t.Helper()
	var m memFile
	writeRaw(t, &m, entries, o)
	return m.b
}

func TestReadsOffsetsBeyond4GB(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "huge.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := winutil.SetSparse(f, true); err != nil {
		t.Skipf("no sparse files here: %v", err)
	}
	const hole = 4<<30 + 512<<20 // 4.5 GB that are never written
	size := writeRaw(t, f, []rawEntry{
		{name: "big.bin", hole: hole},
		{name: "small.txt", data: []byte("small")},
	}, rawOpts{})

	a, err := Read(f, size)
	if err != nil {
		t.Fatal(err)
	}
	big, small := a.Entries[0], a.Entries[1]
	if big.Size != hole || big.CompressedSize != hole {
		t.Fatalf("big.bin sizes %d/%d, want %d", big.CompressedSize, big.Size, int64(hole))
	}
	if small.Start <= 4<<30 || big.End != small.Start {
		t.Fatalf("small.txt starts at %d, big.bin ends at %d", small.Start, big.End)
	}
	if a.LargestCompressed != hole || a.Total != hole+5 {
		t.Fatalf("largest %d, total %d", a.LargestCompressed, a.Total)
	}
	checkRanges(t, a)

	n, err := Need(f, size, TailGuess)
	if err != nil {
		t.Fatal(err)
	}
	if n != size-a.DirStart {
		t.Fatalf("need %d, want %d", n, size-a.DirStart)
	}
}

func TestDataBeforeTheArchiveShiftsTheOffsets(t *testing.T) {
	b := raw(t, rawOpts{prefix: 1000}, rawEntry{name: "a.txt", data: []byte("a")}, rawEntry{name: "b.txt", data: []byte("b")})
	a := readBytes(t, b)
	if a.Entries[0].Start != 1000 {
		t.Fatalf("first Entry at %d, want 1000", a.Entries[0].Start)
	}
	checkRanges(t, a)
}

func TestTheseArchivesCantBeDownloadedAsAFolder(t *testing.T) {
	file := func(name string) rawEntry { return rawEntry{name: name, data: []byte("x")} }
	cases := []struct {
		name string
		zip  []byte
		want string
	}{
		{"not a zip", random(100 << 10), "isn't a ZIP"},
		{"empty file", nil, "isn't a ZIP"},
		{"cut short", goZip(t, "", goFile{name: "a", data: []byte("a")})[30:], "isn't a ZIP"},
		{"split", raw(t, rawOpts{eocdDisk: 1}, file("a")), "split"},
		{"split, Entry on another disk", raw(t, rawOpts{}, rawEntry{name: "a", data: []byte("x"), disk: 1}), "split"},
		{"encrypted", raw(t, rawOpts{}, rawEntry{name: "a", data: []byte("x"), flags: 1}), "encrypted"},
		{"AES", raw(t, rawOpts{}, rawEntry{name: "a", data: []byte("x"), method: 99}), "encrypted"},
		{"unix symlink", raw(t, rawOpts{}, rawEntry{name: "a", data: []byte("b"), madeBy: 3 << 8, external: 0o120777 << 16}), "link"},
		{"windows link", raw(t, rawOpts{}, rawEntry{name: "a", data: []byte("b"), external: 0x400}), "link"},
		{"LZMA", raw(t, rawOpts{}, rawEntry{name: "a", data: []byte("x"), method: 14}), "LZMA"},
		{"unknown method", raw(t, rawOpts{}, rawEntry{name: "a", data: []byte("x"), method: 77}), "method 77"},
		{"absolute", raw(t, rawOpts{}, file("/etc/passwd")), "outside"},
		{"backslash absolute", raw(t, rawOpts{}, file(`\Windows\x`)), "outside"},
		{"drive", raw(t, rawOpts{}, file("C:/x")), "outside"},
		{"drive without slash", raw(t, rawOpts{}, file("C:x")), "outside"},
		{"climbs out", raw(t, rawOpts{}, file("a/../../x")), "outside"},
		{"climbs out with backslashes", raw(t, rawOpts{}, file(`a\..\..\x`)), "outside"},
		{"climbs out at once", raw(t, rawOpts{}, file("../x")), "outside"},
		{"no name", raw(t, rawOpts{}, file("")), "no name"},
		{"same name, other case", raw(t, rawOpts{}, file("Read.me"), file("READ.ME")), "same name"},
		{"same name after clean-up", raw(t, rawOpts{}, file("a?"), file("a*")), "same name"},
		{"file and folder", raw(t, rawOpts{}, file("a"), file("a/b")), "same name"},
		{"overlapping Entries", raw(t, rawOpts{}, rawEntry{name: "a", data: []byte("xxxx")}, rawEntry{name: "b", data: []byte("y"), offset: 1}), "isn't a ZIP"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Read(bytes.NewReader(c.zip), int64(len(c.zip)))
			var u *Unsupported
			if !errors.As(err, &u) {
				t.Fatalf("got %v, want an Unsupported reason", err)
			}
			if !strings.Contains(u.Reason, c.want) {
				t.Fatalf("reason %q doesn't mention %q", u.Reason, c.want)
			}
		})
	}
}

func TestNeedSaysWhenItIsntAZip(t *testing.T) {
	b := random(200 << 10)
	_, err := Need(bytes.NewReader(b), int64(len(b)), int64(len(b)))
	var u *Unsupported
	if !errors.As(err, &u) {
		t.Fatalf("got %v, want an Unsupported reason", err)
	}
}

func TestNamesWindowsCantUseAreChanged(t *testing.T) {
	cases := []struct{ stored, want string }{
		{"CON", "_CON"},
		{"dir/nul.txt", "dir/_nul.txt"},
		{"com1.tar.gz", "_com1.tar.gz"},
		{"LPT9", "_LPT9"},
		{"console.txt", ""},
		{`a<b>c"d|e?f*g.txt`, "a_b_c_d_e_f_g.txt"},
		{"time 12:30.txt", "time 12_30.txt"},
		{"ctrl\x01.txt", "ctrl_.txt"},
		{"trailing. ", "trailing"},
		{"dots.../file", "dots/file"},
		{"...", "_"},
	}
	for _, c := range cases {
		t.Run(c.stored, func(t *testing.T) {
			a := readBytes(t, raw(t, rawOpts{}, rawEntry{name: c.stored, data: []byte("x")}))
			got := a.Entries[0].Name
			if c.want == "" {
				if got != c.stored || len(a.Renamed) != 0 {
					t.Fatalf("%q became %q, renamed %v", c.stored, got, a.Renamed)
				}
				return
			}
			if got != c.want {
				t.Fatalf("%q became %q, want %q", c.stored, got, c.want)
			}
			if want := []Rename{{From: c.stored, To: c.want}}; !reflect.DeepEqual(a.Renamed, want) {
				t.Fatalf("renamed %v, want %v", a.Renamed, want)
			}
			if a.Entries[0].Stored != c.stored {
				t.Fatalf("stored name %q", a.Entries[0].Stored)
			}
		})
	}
}

func TestSeparatorsAndDotFoldersAreNotRenames(t *testing.T) {
	a := readBytes(t, raw(t, rawOpts{}, rawEntry{name: `./a\\b/./c.txt`, data: []byte("x")}))
	if got := a.Entries[0].Name; got != "a/b/c.txt" {
		t.Fatalf("name %q", got)
	}
	if len(a.Renamed) != 0 {
		t.Fatalf("renamed %v", a.Renamed)
	}
}

func TestNamesNotMarkedUTF8AreReadAsCodePage437(t *testing.T) {
	a := readBytes(t, raw(t, rawOpts{}, rawEntry{name: "gr\x81\xe1e.txt", data: []byte("x")}))
	if got := a.Entries[0].Name; got != "grüße.txt" {
		t.Fatalf("name %q", got)
	}
	if len(a.Renamed) != 0 {
		t.Fatalf("decoding counted as a rename: %v", a.Renamed)
	}
	u := readBytes(t, raw(t, rawOpts{}, rawEntry{name: "grüße.txt", data: []byte("x"), flags: 1 << 11}))
	if got := u.Entries[0].Name; got != "grüße.txt" {
		t.Fatalf("UTF-8 name %q", got)
	}
}

func TestTheOutputRoot(t *testing.T) {
	file := func(name string) rawEntry { return rawEntry{name: name, data: []byte("x")} }
	cases := []struct {
		name    string
		entries []rawEntry
		want    string
	}{
		{"one folder with its own Entry", []rawEntry{file("game/"), file("game/a"), file("game/b/c")}, "game"},
		{"one folder, implied", []rawEntry{file("game/a"), file("game/b/c")}, "game"},
		{"cleaned name", []rawEntry{file("con/a")}, "_con"},
		{"two folders", []rawEntry{file("a/x"), file("b/y")}, ""},
		{"a folder and a file", []rawEntry{file("a/x"), file("readme")}, ""},
		{"one file", []rawEntry{file("readme")}, ""},
		{"only an empty folder", []rawEntry{file("a/")}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := readBytes(t, raw(t, rawOpts{}, c.entries...)).Root; got != c.want {
				t.Fatalf("root %q, want %q", got, c.want)
			}
		})
	}
}

func TestAnEmptyArchiveHasNoEntries(t *testing.T) {
	a := readBytes(t, goZip(t, ""))
	if len(a.Entries) != 0 || a.Root != "" || a.Total != 0 {
		t.Fatalf("%+v", a)
	}
}

func TestNeedSettlesWhenTheZIP64RecordIsLong(t *testing.T) {
	b := raw(t, rawOpts{zip64: true, end64Pad: 300}, rawEntry{name: "pad.bin", data: random(100 << 10)}, rawEntry{name: "a.txt", data: []byte("a")})
	size := int64(len(b))
	have := int64(endLen)
	for range 10 {
		n, err := Need(tailOnly{b, have}, size, have)
		if err != nil {
			t.Fatal(err)
		}
		if n <= have {
			break
		}
		have = n
	}
	a, err := Read(tailOnly{b, have}, size)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if have != size-a.DirStart {
		t.Fatalf("settled on %d, want %d", have, size-a.DirStart)
	}
}

func TestAFolderAttributeMakesAFolder(t *testing.T) {
	a := readBytes(t, raw(t, rawOpts{},
		rawEntry{name: "assets", external: 0x10},
		rawEntry{name: "lib", madeBy: 3 << 8, external: 0o040755 << 16},
		rawEntry{name: "assets/x", data: []byte("x")},
	))
	for _, e := range a.Entries[:2] {
		if !e.Dir {
			t.Fatalf("%s isn't a folder", e.Name)
		}
	}
}

func TestAStoredEntryMustBeAsLongAsItsFile(t *testing.T) {
	b := raw(t, rawOpts{}, rawEntry{name: "a", data: []byte("abc")})
	// Make the central directory's uncompressed size disagree.
	at := bytes.LastIndex(b, []byte{0x50, 0x4b, 0x01, 0x02})
	binary.LittleEndian.PutUint32(b[at+24:], 999)
	_, err := Read(bytes.NewReader(b), int64(len(b)))
	var u *Unsupported
	if !errors.As(err, &u) {
		t.Fatalf("got %v, want an Unsupported reason", err)
	}
}

func TestNTFSTimesFarFrom1970(t *testing.T) {
	for _, want := range []time.Time{time.Date(1700, 1, 2, 3, 4, 5, 0, time.UTC), time.Date(2400, 6, 7, 8, 9, 10, 0, time.UTC)} {
		ticks := uint64(want.Unix()+11644473600) * 1e7
		f := le16(le16(le32(nil, 0), 1), 24)
		f = le64(le64(le64(f, ticks), 0), 0)
		got, ok := ntfsTime(f)
		if !ok || !got.Equal(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
