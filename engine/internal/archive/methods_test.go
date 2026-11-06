package archive

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"hash/crc32"
	"io"
	"maps"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// zstdZip is a ZIP of files compressed with Zstandard (method 93), as WinZip
// and 7-Zip's Zstandard builds write them. opts go to the encoder.
func zstdZip(t *testing.T, files map[string][]byte, opts ...zstd.EOption) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	w.RegisterCompressor(Zstd, func(out io.Writer) (io.WriteCloser, error) { return zstd.NewWriter(out, opts...) })
	for _, name := range slices.Sorted(maps.Keys(files)) {
		fw, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: Zstd})
		if err != nil {
			t.Fatal(err)
		}
		fw.Write(files[name])
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func someFiles(seed int64) map[string][]byte {
	r := rand.New(rand.NewSource(seed))
	random := make([]byte, 300<<10)
	r.Read(random)
	return map[string][]byte{
		"text.txt":    bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 20000),
		"random.bin":  random,
		"dir/far.bin": append(append(random[:100<<10:100<<10], bytes.Repeat([]byte("A"), 70000)...), random[:100<<10]...),
		"empty.txt":   nil,
	}
}

// extractWhole has the whole archive arrive and extracts it into a new folder.
func extractWhole(t *testing.T, data []byte) (string, Result) {
	t.Helper()
	d := newDownload(t, data)
	a := d.tail()
	d.arrive(0, int64(len(data)))
	dir := t.TempDir()
	x := extractor(t, a, d.f, dir)
	defer x.Close()
	return dir, extract(t, x, d.missing)
}

func checkFiles(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: %d bytes, not the %d put in", name, len(got), len(want))
		}
	}
}

func TestZstandardEntriesAreExtracted(t *testing.T) {
	files := someFiles(1)
	data := zstdZip(t, files)
	dir, res := extractWhole(t, data)
	if len(res.Failed) > 0 {
		t.Fatalf("failed: %v", res.Failed[0])
	}
	checkFiles(t, dir, files)
}

// The deprecated number 20 for Zstandard is read the same.
func TestZstandardUnderItsOldNumberIsExtracted(t *testing.T) {
	files := someFiles(2)
	data := zstdZip(t, files)
	a, err := Read(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	// Renumber every header from 93 to 20.
	for _, e := range a.Entries {
		data[e.Start+8] = byte(zstdOld)
	}
	for i := a.DirStart; i+4 <= int64(len(data)); i++ {
		if bytes.Equal(data[i:i+4], []byte{0x50, 0x4b, 0x01, 0x02}) {
			data[i+10] = byte(zstdOld)
		}
	}
	dir, res := extractWhole(t, data)
	if len(res.Failed) > 0 {
		t.Fatalf("failed: %v", res.Failed[0])
	}
	checkFiles(t, dir, files)
}

func TestAZstandardLargeEntryStreams(t *testing.T) {
	lowerLarge(t, 64<<10, 64<<10)
	files := someFiles(3)
	data := zstdZip(t, files)
	d := newDownload(t, data)
	a := d.tail()
	dir := t.TempDir()
	x := extractor(t, a, d.f, dir)
	defer x.Close()
	// In order, 16 KiB at a time, extracting after each.
	var res Result
	for at := int64(0); at < a.DirStart; at += 16 << 10 {
		d.arrive(at, min(at+16<<10, a.DirStart))
		r := extract(t, x, d.missing)
		res.Failed = append(res.Failed, r.Failed...)
	}
	if len(res.Failed) > 0 {
		t.Fatalf("failed: %v", res.Failed[0])
	}
	if !x.Finished() {
		t.Fatal("not finished")
	}
	checkFiles(t, dir, files)
}

func TestAWrongZstandardByteFailsTheEntry(t *testing.T) {
	files := map[string][]byte{"a.txt": bytes.Repeat([]byte("zstandard "), 50000)}
	data := zstdZip(t, files)
	a, _ := Read(bytes.NewReader(data), int64(len(data)))
	e := a.Entries[0]
	data[e.End-40] ^= 0xff
	dir, res := extractWhole(t, data)
	if len(res.Failed) != 1 || len(res.Done) != 0 {
		t.Fatalf("done %d, failed %d", len(res.Done), len(res.Failed))
	}
	if left := leftovers(t, dir); len(left) > 0 {
		t.Fatalf("left %v", left)
	}
}

// rawZstdZip is a ZIP of one Entry holding "x" in the Zstandard frame given:
// magic, then header and block as hdr.
func rawZstdZip(t *testing.T, name string, hdr ...byte) []byte {
	t.Helper()
	frame := append([]byte{0x28, 0xb5, 0x2f, 0xfd}, hdr...)
	frame = append(frame, 1<<3|1, 0, 0, 'x') // one last raw block of 1 byte
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	fw, err := w.CreateRaw(&zip.FileHeader{Name: name, Method: Zstd, CRC32: crc32.ChecksumIEEE([]byte("x")),
		CompressedSize64: uint64(len(frame)), UncompressedSize64: 1})
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(frame)
	w.Close()
	return buf.Bytes()
}

// Bytes that are right but can't be decoded here stop the download with why,
// rather than failing the Entry, which would have it fetched again for nothing.
func TestZstandardFramesThatCantBeDecodedHereStopWithAReason(t *testing.T) {
	if zstdMaxWindow >= 1<<29 {
		t.Fatal("the window below isn't over the limit")
	}
	cases := []struct {
		name string
		hdr  []byte
		want string
	}{
		// No single segment, window log 29 (exponent 19 over 10): 512 MiB.
		{"window", []byte{0x00, 19 << 3}, "512 MiB Zstandard window"},
		// Single segment, an 8-byte content size of 1 GiB: that's its window.
		{"single segment", []byte{0xe0, 0, 0, 0, 0x40, 0, 0, 0, 0}, "1024 MiB Zstandard window"},
		// A 1-byte dictionary ID.
		{"dictionary", []byte{0x01, 10 << 3, 7}, "dictionary"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := rawZstdZip(t, "a.txt", c.hdr...)
			d := newDownload(t, data)
			a := d.tail()
			d.arrive(0, int64(len(data)))
			x := extractor(t, a, d.f, t.TempDir())
			defer x.Close()
			res, err := x.Extract(context.Background(), d.missing)
			var u *Unsupported
			if !errors.As(err, &u) || !strings.Contains(u.Reason, c.want) || len(res.Failed) > 0 || len(res.Done) > 0 {
				t.Fatalf("err %v, failed %d, done %d", err, len(res.Failed), len(res.Done))
			}
		})
	}
	// A small window is fine.
	data := rawZstdZip(t, "a.txt", 0x00, 10<<3)
	if _, res := extractWhole(t, data); len(res.Done) != 1 {
		t.Fatalf("a 1 KiB window: done %d, failed %d", len(res.Done), len(res.Failed))
	}
}

// A large Entry is checked the same before any of its bytes are freed.
func TestALargeZstandardEntryWithTooBigAWindowFreesNothing(t *testing.T) {
	// Large from 4 bytes, so the 10-byte frame streams; freed every 1 MiB, as
	// the decoder sees the header long before a real FreeStep.
	lowerLarge(t, 4, 1<<20)
	data := rawZstdZip(t, "big.bin", 0x00, 19<<3)
	d := newDownload(t, data)
	a := d.tail()
	if !large(a.Entries[0]) {
		t.Fatal("the Entry isn't large")
	}
	d.arrive(0, int64(len(data)))
	x := extractor(t, a, d.f, t.TempDir())
	defer x.Close()
	res, err := x.Extract(context.Background(), d.missing)
	var u *Unsupported
	if !errors.As(err, &u) || len(res.Freed) > 0 || len(res.Failed) > 0 {
		t.Fatalf("err %v, freed %v, failed %d", err, res.Freed, len(res.Failed))
	}
}

// testdata/deflate64.zip is from 7-Zip (7z a -tzip -mm=Deflate64 -mx=9): a
// 40,000-byte random block twice (a match 40,000 back, past DEFLATE's 32 KiB
// window), a run of 70,000 bytes (a match longer than 258), and more.
func TestDeflate64EntriesAreExtracted(t *testing.T) {
	data, err := os.ReadFile("testdata/deflate64.zip")
	if err != nil {
		t.Fatal(err)
	}
	a, err := Read(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var far Entry
	for _, e := range a.Entries {
		if !e.Dir && e.Method != Deflate64 {
			t.Fatalf("%s: method %d", e.Name, e.Method)
		}
		if e.Name == "d/far.bin" {
			far = e
		}
	}
	// The archive does use what DEFLATE lacks: plain inflate can't read it.
	if bad, _ := copyEntry(context.Background(), Entry{Method: Deflate, Size: far.Size, CRC32: far.CRC32}, deflateData(t, data, far), discardFile(t)); bad == "" {
		t.Fatal("plain DEFLATE read it: the test archive doesn't use Deflate64's longer matches")
	}
	dir, res := extractWhole(t, data)
	if len(res.Failed) > 0 {
		t.Fatalf("failed: %v", res.Failed[0])
	}
	// Each file's CRC32 and size matched, so its bytes are right.
	if res.Done == nil || !fileExists(filepath.Join(dir, "d", "far.bin")) || !fileExists(filepath.Join(dir, "small.txt")) {
		t.Fatalf("done %d", len(res.Done))
	}
}

func TestADeflate64LargeEntryStreams(t *testing.T) {
	lowerLarge(t, 16<<10, 16<<10)
	data, err := os.ReadFile("testdata/deflate64.zip")
	if err != nil {
		t.Fatal(err)
	}
	d := newDownload(t, data)
	a := d.tail()
	x := extractor(t, a, d.f, t.TempDir())
	defer x.Close()
	for at := int64(0); at < a.DirStart; at += 8 << 10 {
		d.arrive(at, min(at+8<<10, a.DirStart))
		if r := extract(t, x, d.missing); len(r.Failed) > 0 {
			t.Fatalf("failed: %v", r.Failed[0])
		}
	}
	if !x.Finished() {
		t.Fatal("not finished")
	}
}

// A read of the archive file that fails is the disk's fault, not the Entry's,
// whatever the method.
func TestAFailingReadIsntTheEntrysFault(t *testing.T) {
	for _, m := range []uint16{Store, Deflate, Deflate64, Zstd} {
		src := io.MultiReader(bytes.NewReader([]byte{1, 2, 3}), failing{})
		bad, err := copyEntry(context.Background(), Entry{Method: m, Size: 1 << 20}, src, discardFile(t))
		if bad != "" || err == nil {
			t.Errorf("method %d: bad %q, err %v", m, bad, err)
		}
	}
}

type failing struct{}

func (failing) Read([]byte) (int, error) {
	return 0, &os.PathError{Op: "read", Path: "big.zip.dms.part", Err: os.ErrClosed}
}

func deflateData(t *testing.T, data []byte, e Entry) io.Reader {
	t.Helper()
	nameLen := int64(data[e.Start+26]) | int64(data[e.Start+27])<<8
	extraLen := int64(data[e.Start+28]) | int64(data[e.Start+29])<<8
	start := e.Start + 30 + nameLen + extraLen
	return bytes.NewReader(data[start : start+e.CompressedSize])
}

func discardFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
