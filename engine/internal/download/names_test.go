package download

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFinalNameReplacesOnlyTheFileChosen(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.iso"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := finalName("", dir, "a.iso"); got != "a (1).iso" {
		t.Errorf("no choice: %q", got)
	}
	if got := finalName(filepath.Join(dir, "a.iso"), dir, "a.iso"); got != "a.iso" {
		t.Errorf("chosen: %q", got)
	}
	// A chosen file in another folder says nothing about this one.
	if got := finalName(filepath.Join(dir, "other", "a.iso"), dir, "a.iso"); got != "a (1).iso" {
		t.Errorf("other folder: %q", got)
	}
}
