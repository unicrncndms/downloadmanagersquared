package download

import "testing"

// Renaming a download into or out of a part name moves it between Sets
// (setOf and the Summary's group read the name each time, nothing caches it).
func TestARenameJoinsAndLeavesASet(t *testing.T) {
	d := New(&testEnv{}, State{ID: "a", URL: "https://x.example/a", Dir: `C:\D`, Filename: "film.mkv", Status: StatusCompleted, Size: 1})
	rename := func(name string) {
		d.mu.Lock()
		d.st.Filename = name
		d.mu.Unlock()
		d.changed()
	}
	if key, _, _, _ := setOf(d); key != "" || d.Summary().Group != "" {
		t.Fatal("a plain file is in a Set")
	}
	rename("film.part2.rar")
	key, set, part, _ := setOf(d)
	if key == "" || set != "film.rar" || part != 2 || d.Summary().GroupName != "film.rar" {
		t.Fatalf("after the rename: key %q set %q part %d group %q", key, set, part, d.Summary().GroupName)
	}
	rename("film.mkv")
	if key, _, _, _ := setOf(d); key != "" || d.Summary().Group != "" {
		t.Fatal("still in the Set after the rename back")
	}
}

// A finished download's Summary is kept, but not past a change of what it reads.
func TestAKeptSummaryFollowsItsHashResultAndTheKeepSetting(t *testing.T) {
	env := &testEnv{keeping: KeepAuto}
	d := New(env, State{ID: "a", URL: "https://x.example/a", Dir: `C:\D`, Filename: "film.zip", Status: StatusCompleted, Size: 1, Probed: true, RangeOK: true, Folder: &Folder{On: true, Into: `C:`}})
	first := d.Summary()
	if first.Label == "Checksum mismatch" || first.Folder == nil || first.Folder.Keep != KeepAuto {
		t.Fatalf("first summary: %q, %+v", first.Label, first.Folder)
	}
	d.Summary() // the kept one
	env.keeping = KeepFree
	if got := d.Summary().Folder.Keep; got == first.Folder.Keep {
		t.Fatalf("the keep setting changed but the summary still says %q", got)
	}
	d.mu.Lock()
	d.st.HashResult = "expected abc, got def"
	d.mu.Unlock()
	d.changed()
	if got := d.Summary().Label; got != "Checksum mismatch" {
		t.Fatalf("after a hash result the label is %q", got)
	}
	d.mu.Lock()
	d.checking = true
	d.mu.Unlock()
	if got := d.Summary().Label; got == "Checksum mismatch" {
		t.Fatal("a download being checked still shows the old result")
	}
}
