package winutil

import (
	"os"
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
