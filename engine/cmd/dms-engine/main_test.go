package main

import (
	"os"
	"path/filepath"
	"strconv"
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

// Uninstall names the folders that hold unfinished downloads, once each.
func TestUnfinishedFoldersAreThoseWithAPartFile(t *testing.T) {
	data, a, b := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(data, "downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	save := func(id, dir, name string, part bool) {
		js := `{"id":"` + id + `","dir":` + strconv.Quote(dir) + `,"filename":"` + name + `"}`
		os.WriteFile(filepath.Join(data, "downloads", id+".json"), []byte(js), 0o644)
		if part {
			os.WriteFile(filepath.Join(dir, name+".dms.part"), nil, 0o644)
		}
	}
	save("1", a, "x.iso", true)
	save("2", a, "y.iso", true)
	save("3", b, "z.iso", false) // finished: no part file
	got := unfinishedFolders(data)
	if len(got) != 1 || got[0] != a {
		t.Fatalf("unfinishedFolders = %v, want [%s]", got, a)
	}
}
