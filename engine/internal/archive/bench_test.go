package archive

import (
	"archive/zip"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"downloadmanagersquared/engine/internal/winutil"
)

// benchExtract extracts the archive at path into a fresh folder per run,
// everything on disk, as a finished download is.
func benchExtract(b *testing.B, path string) {
	src, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	a, err := Read(f, int64(len(src)))
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		dir, err := os.MkdirTemp(b.TempDir(), "out")
		if err != nil {
			b.Fatal(err)
		}
		// Extracting frees the archive's bytes, so each run gets a copy.
		part := newBenchPart(b, src)
		x, err := NewExtractor(a, part, dir, nil)
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		for {
			r, err := x.Extract(b.Context(), nil)
			if err != nil || len(r.Failed) > 0 {
				b.Fatalf("Extract: %v %v", err, r.Failed)
			}
			if !r.More {
				break
			}
		}
		b.StopTimer()
		x.Close()
		part.Close()
		os.RemoveAll(dir)
	}
}

func newBenchPart(b *testing.B, data []byte) *os.File {
	f, err := os.Create(filepath.Join(b.TempDir(), "bench.part"))
	if err != nil {
		b.Fatal(err)
	}
	if err := winutil.SetSparse(f, true); err != nil {
		b.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		b.Fatal(err)
	}
	return f
}

// benchZip writes a deflate ZIP of files text-like files of size bytes each.
func benchZip(b *testing.B, files, size int) string {
	path := filepath.Join(b.TempDir(), "bench.zip")
	f, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	w := zip.NewWriter(f)
	words := []string{"download", "manager", "squared", "archive", "extract", "range", "engine", "buffer", "the", "of", "and"}
	rng := rand.New(rand.NewSource(1))
	data := make([]byte, 0, size+16)
	for i := range files {
		data = data[:0]
		for len(data) < size {
			data = append(data, words[rng.Intn(len(words))]...)
			data = append(data, ' ')
		}
		fw, err := w.Create(fmt.Sprintf("d%d/f%d.txt", i%100, i))
		if err != nil {
			b.Fatal(err)
		}
		fw.Write(data[:size])
	}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	f.Close()
	return path
}

// 50k small files: the per-file cost (buffers, decoder, fsync).
func BenchmarkExtractZipSmallFiles(b *testing.B) { benchExtract(b, benchZip(b, 50000, 1<<10)) }

// About 100 MB of deflate in four files: decoder speed.
func BenchmarkExtractZipDeflate(b *testing.B) { benchExtract(b, benchZip(b, 4, 25<<20)) }

// The checked-in 7z fixture is tiny: it shows the per-archive cost only.
func BenchmarkExtract7zNonsolid(b *testing.B) { benchExtract(b, "testdata/nonsolid.7z") }
