package winutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCanWriteLooksAtTheNearestFolderThatExists(t *testing.T) {
	dir := t.TempDir()
	if !CanWrite(filepath.Join(dir, "not", "made", "yet")) {
		t.Error("can't write below a temp folder")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the check left %d entries behind", len(entries))
	}
	if CanWrite(filepath.Join(os.Getenv("SystemRoot"), "System32", "dms-test")) && !elevated() {
		t.Error("an unelevated test can write into System32")
	}
}

func elevated() bool {
	f, err := os.Create(filepath.Join(os.Getenv("SystemRoot"), "dms-elevation-probe"))
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

func TestHoldUnchangedBlocksWritesAndRenamesUntilClosed(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "dms-engine.exe")
	if err := os.WriteFile(exe, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	hold, err := HoldUnchanged(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("b"), 0o644); err == nil {
		t.Error("wrote to a held file")
	}
	if err := os.Rename(exe, exe+".old"); err == nil {
		t.Error("renamed a held file")
	}
	if b, err := os.ReadFile(exe); err != nil || string(b) != "a" {
		t.Errorf("reading a held file = %q, %v", b, err)
	}
	hold.Close()
	if err := os.WriteFile(exe, []byte("b"), 0o644); err != nil {
		t.Errorf("writing after release: %v", err)
	}
}

func TestHoldUnchangedStillLetsTheHeldExeStart(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(os.Getenv("SystemRoot"), "System32", "where.exe"))
	if err != nil {
		t.Fatal(err)
	}
	copy := filepath.Join(t.TempDir(), "where-copy.exe")
	if err := os.WriteFile(copy, src, 0o755); err != nil {
		t.Fatal(err)
	}
	hold, err := HoldUnchanged(copy)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	if err := os.WriteFile(copy, []byte("b"), 0o755); err == nil {
		t.Error("wrote to a held exe")
	}
	if err := exec.Command(copy, "/?").Run(); err != nil {
		if _, exited := err.(*exec.ExitError); !exited {
			t.Fatalf("starting the held exe: %v", err)
		}
	}
}
