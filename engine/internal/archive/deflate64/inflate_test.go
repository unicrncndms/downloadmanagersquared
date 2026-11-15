package deflate64

import (
	"bytes"
	"compress/flate"
	"errors"
	"io"
	"testing"
)

// Short matches only (under 258 bytes, within 32 KiB) read the same in Deflate
// and Deflate64, so Go's own compressor makes known streams: stored, fixed and
// dynamic Huffman blocks.
func deflated(t *testing.T, level int, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w, _ := flate.NewWriter(&b, level)
	w.Write(data)
	w.Close()
	return b.Bytes()
}

// words is text with matches but no run long enough to reach length code 285.
func words() []byte {
	vocab := []string{"deflate", "sixty", "four", "reads", "what", "windows", "writes", "large", "files", "zip"}
	var b []byte
	for x := uint32(1); len(b) < 30000; x = x*1664525 + 1013904223 {
		b = append(b, vocab[x>>24%10]...)
		b = append(b, ' ')
	}
	return b
}

func TestInflateKnownStreams(t *testing.T) {
	text := words()
	var varied []byte
	for i := 0; i < 20000; i++ {
		varied = append(varied, byte(i*i>>3), byte(i%251))
	}
	for name, c := range map[string]struct {
		level int
		data  []byte
	}{
		"stored":  {flate.NoCompression, text},
		"fixed":   {flate.BestSpeed, []byte("a short text, a short text")},
		"dynamic": {flate.BestCompression, text},
		"varied":  {flate.DefaultCompression, varied},
	} {
		got, err := io.ReadAll(NewReader(bytes.NewReader(deflated(t, c.level, c.data))))
		if err != nil || !bytes.Equal(got, c.data) {
			t.Errorf("%s: %d of %d bytes back, err %v", name, len(got), len(c.data), err)
		}
	}
}

// A truncated stream is an error, never a clean end, and noise is corrupt input.
func TestInflateRefusesBadInput(t *testing.T) {
	text := words()
	stream := deflated(t, flate.BestCompression, text)
	for _, n := range []int{0, 1, len(stream) / 2, len(stream) - 1} {
		_, err := io.ReadAll(NewReader(bytes.NewReader(stream[:n])))
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("cut at %d of %d: err %v, want unexpected EOF", n, len(stream), err)
		}
	}
	// Block type 3 does not exist; a stored block whose length and its complement disagree.
	for name, bad := range map[string][]byte{"block type 3": {0x07}, "stored length": {0x01, 0x05, 0x00, 0x00, 0x00, 'x'}} {
		_, err := io.ReadAll(NewReader(bytes.NewReader(bad)))
		var ce CorruptInputError
		if !errors.As(err, &ce) {
			t.Errorf("%s: err %v, want CorruptInputError", name, err)
		}
	}
}

// Any bytes at all end in output or an error: never a panic, never without end.
// Run longer with: go test ./internal/archive/deflate64/ -fuzz FuzzInflate
func FuzzInflate(f *testing.F) {
	f.Add(deflated(&testing.T{}, flate.BestCompression, words()[:2000]))
	f.Add([]byte{0x07})
	f.Add([]byte{0x01, 0x05, 0x00, 0x00, 0x00, 'x'})
	f.Fuzz(func(t *testing.T, in []byte) {
		io.Copy(io.Discard, io.LimitReader(NewReader(bytes.NewReader(in)), 64<<20))
	})
}
