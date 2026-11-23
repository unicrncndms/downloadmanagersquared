package winutil

import (
	"os"
	"path/filepath"
	"testing"
)

// A file that is there is selected; one that is gone opens its folder; a
// folder that is gone is an error, not Explorer's Desktop.
func TestShowInFolderOfAMissingFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := showCmdLine(file); err != nil || got != `explorer.exe /select,"`+file+`"` {
		t.Errorf("present file: %q, %v", got, err)
	}
	gone := filepath.Join(dir, "gone.bin")
	if got, err := showCmdLine(gone); err != nil || got != `explorer.exe "`+dir+`"` {
		t.Errorf("missing file: %q, %v", got, err)
	}
	if _, err := showCmdLine(filepath.Join(dir, "no", "gone.bin")); err == nil {
		t.Error("missing folder gave no error")
	}
}

// Opening a file that is gone says so in plain words.
func TestOpenAMissingFile(t *testing.T) {
	err := OpenFile(filepath.Join(t.TempDir(), "gone.bin"))
	if err == nil || err.Error() != "gone.bin is not there any more: it was moved or deleted outside DMS²" {
		t.Errorf("got %v", err)
	}
}
