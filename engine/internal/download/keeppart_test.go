package download

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRenameDelay(t *testing.T) {
	for n, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 5: 16 * time.Second, 6: 30 * time.Second, 40: 30 * time.Second} {
		if got := renameDelay(n); got != want {
			t.Errorf("renameDelay(%d) = %v, want %v", n, got, want)
		}
	}
}

// A part still open (a Set's reader) can't be renamed on Windows: keepPart
// holds off instead of trying again at once, then names it once it is let go.
func TestKeepPartBacksOff(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only Windows refuses to rename an open file")
	}
	dir := t.TempDir()
	d := &Download{st: State{ID: "x", Dir: dir, Filename: "a.part1.rar"}}
	part := d.partPath()
	if err := os.WriteFile(part, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(part)
	if err != nil {
		t.Fatal(err)
	}
	d.keepPart(part)
	d.keepPart(part) // held off: not a second failure
	if d.renameFails != 1 {
		t.Fatalf("renameFails = %d, want 1", d.renameFails)
	}
	f.Close()
	d.keepPart(part)
	if !exists(part) {
		t.Fatal("renamed during the hold-off")
	}
	d.renameNext = time.Time{}
	d.keepPart(part)
	if _, err := os.Stat(filepath.Join(dir, "a.part1.rar")); err != nil || d.renameFails != 0 {
		t.Fatalf("not named after the reader let go: %v, fails %d", err, d.renameFails)
	}
}
