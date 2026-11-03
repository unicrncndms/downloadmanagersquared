package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteJSONReplacesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "settings.json")
	if err := WriteJSON(path, map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(path, map[string]int{"a": 2}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "{\n  \"a\": 2\n}" {
		t.Fatalf("file holds %q", b)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("the temp file is left over: %v", err)
	}
}

func TestAFailedWriteLeavesTheOldFileIntact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The temp file can't be created: a folder sits where it would go.
	if err := os.Mkdir(path+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(path, map[string]int{"a": 1}); err == nil {
		t.Fatal("writing succeeded")
	}
	// Nothing to encode: the old file stays too.
	if err := WriteJSON(path, make(chan int)); err == nil {
		t.Fatal("encoding a channel succeeded")
	}
	if b, _ := os.ReadFile(path); string(b) != "old" {
		t.Fatalf("file holds %q, want the old content", b)
	}
}

func TestRemoveStaleDeletesTempFilesBelowDir(t *testing.T) {
	dir := t.TempDir()
	keep := []string{"settings.json", "downloads/a.json", "tunnels/x.conf"}
	stale := []string{"settings.json.tmp", "downloads/a.json.tmp", "tunnels/x.conf.tmp"}
	for _, f := range append(keep, stale...) {
		p := filepath.Join(dir, f)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
	}
	RemoveStale(dir)
	for _, f := range keep {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s is gone: %v", f, err)
		}
	}
	for _, f := range stale {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("%s is still there", f)
		}
	}
}
