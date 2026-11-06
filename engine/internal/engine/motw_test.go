package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMarkTorrentMarksEveryFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Some torrent")
	for _, p := range []string{"a.mkv", "sub/b.srt"} {
		p = filepath.Join(dir, p)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("x"), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	markTorrent("t1", dir)
	for _, p := range []string{"a.mkv", "sub/b.srt"} {
		got, err := os.ReadFile(filepath.Join(dir, p) + ":Zone.Identifier")
		if err != nil || string(got) != "[ZoneTransfer]\r\nZoneId=3\r\n" {
			t.Errorf("%s: mark %q, %v", p, got, err)
		}
	}
}
