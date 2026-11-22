package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// With a file of that name already in the folder, the onExists setting
// decides: rename adds as before, skip adds nothing, overwrite and ask hold
// the download for an answer, and a replace is for that one file only.
func TestExistingFileFollowsTheSetting(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	e.settings.MaxParallel = 0
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.iso"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	add := func() map[string]string {
		t.Helper()
		r, err := e.addAsking(AddRequest{URL: "https://example.com/a.iso", Dir: dir})
		if err != nil {
			t.Fatal(err)
		}
		return r.(map[string]string)
	}
	listed := func() int { return len(e.queue.List()) }

	if r := add(); r["id"] == "" || listed() != 1 {
		t.Fatalf("rename: %v", r)
	}
	e.settings.OnExists = OnExistsSkip
	if r := add(); r["skipped"] == "" || listed() != 1 {
		t.Fatalf("skip: %v", r)
	}
	e.settings.OnExists = OnExistsOverwrite
	r := add()
	ask := r["ask"]
	if ask == "" || listed() != 1 {
		t.Fatalf("overwrite should hold: %v", r)
	}
	if _, err := e.answerAsk(ask, "keepboth"); err == nil {
		t.Fatal("an answer it did not offer was taken")
	}
	if _, err := e.answerAsk(ask, "replace"); err != nil || listed() != 2 {
		t.Fatalf("replace: %v, listed %d", err, listed())
	}
	var replacing int
	for _, en := range e.queue.List() {
		if s := en.Summary(); s.Filename == "a.iso" {
			replacing++
		}
	}
	if replacing != 1 {
		t.Fatalf("%d downloads are to replace a.iso, want 1", replacing)
	}
	if _, err := e.answerAsk(ask, "replace"); err == nil {
		t.Fatal("answered twice")
	}
	e.settings.OnExists = OnExistsAsk
	ask = add()["ask"]
	if _, err := e.answerAsk(ask, "cancel"); err != nil || listed() != 2 {
		t.Fatalf("cancel added it: %v", err)
	}
	ask = add()["ask"]
	if _, err := e.answerAsk(ask, "rename"); err != nil || listed() != 3 {
		t.Fatalf("keep both: %v", err)
	}
}
