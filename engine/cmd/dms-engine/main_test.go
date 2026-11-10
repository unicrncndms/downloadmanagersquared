package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The log starts a new file past its limit while the engine runs, keeping
// the one before as .old.
func TestTheLogRotatesWhileRunning(t *testing.T) {
	l := &logFile{path: filepath.Join(t.TempDir(), "engine.log"), max: 100}
	if err := l.open(); err != nil {
		t.Fatal(err)
	}
	defer func() { l.f.Close() }()
	first := strings.Repeat("a", 101) + "\n"
	for _, line := range []string{first, "second\n"} {
		if _, err := l.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if old, _ := os.ReadFile(l.path + ".old"); string(old) != first {
		t.Fatalf(".old holds %q", old)
	}
	if cur, _ := os.ReadFile(l.path); string(cur) != "second\n" {
		t.Fatalf("the log holds %q", cur)
	}
}
