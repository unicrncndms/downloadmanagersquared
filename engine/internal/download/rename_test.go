package download

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanFileName(t *testing.T) {
	for in, want := range map[string]string{
		`  my:file?.zip `: "my_file_.zip",
		`a/b\c.txt`:       "a_b_c.txt",
		"CON.txt":         "_CON.txt",
		"report. ":        "report",
		"  ":              "",
	} {
		if got := CleanFileName(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// A paused download's partial file follows its new name, a name already
// taken is numbered, and the finished file takes the new name.
func TestRenameAPausedDownload(t *testing.T) {
	data := buildZip(t, manyFiles("", 40, 256<<10))
	srv := newZipServer(data)
	defer srv.Close()
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 5*time.Millisecond)}, main: "eth"}
	d := startUntil(t, env, State{ID: "rn1", URL: srv.URL + "/dl", Dir: dir, Size: -1}, func(d *Download) bool {
		s := d.Summary()
		return s.Done > 0 && s.Status == StatusRunning
	})
	if err := d.Rename("x"); err == nil {
		t.Fatal("renamed a running download")
	}
	d.Stop(StatusPaused)
	if err := d.Rename("  "); err == nil {
		t.Fatal("took an empty name")
	}
	if err := os.WriteFile(filepath.Join(dir, "taken.bin"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.Rename("taken.bin"); err != nil {
		t.Fatal(err)
	}
	if s := d.Summary(); s.Filename != "taken (1).bin" || !s.Actions.Rename {
		t.Fatalf("name %q, rename offered %v", s.Filename, s.Actions.Rename)
	}
	if err := d.Rename("my:file.bin"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "my_file.bin"+partSuffix)); err != nil {
		t.Fatalf("the .part did not follow: %v (%v)", err, names(t, dir))
	}
	st := d.State()
	st.Status = StatusQueued
	d.dropParked()
	d2 := runToEnd(t, env, st)
	if s := d2.Summary(); s.Status != StatusCompleted || s.Filename != "my_file.bin" {
		t.Fatalf("status %s name %q: %s", s.Status, s.Filename, s.Error)
	}
	checkFile(t, filepath.Join(dir, "my_file.bin"), data)
	if err := d2.Rename("again.bin"); err == nil {
		t.Fatal("renamed a complete download")
	}
}

// A download as folder and a split archive's part are not renamed, and the
// summary says why instead of offering it.
func TestRenameRefusedWhereTheNameIsNotOurs(t *testing.T) {
	d := New(&testEnv{}, State{ID: "rn2", URL: "http://x.test/a.zip", Dir: t.TempDir(), Filename: "a.zip", Folder: &Folder{On: true}, Status: StatusPaused})
	if err := d.Rename("b.zip"); err == nil {
		t.Fatal("renamed a download as folder")
	}
	if s := d.Summary(); s.Actions.Rename || s.RenameNote != noRenameFolder {
		t.Fatalf("offered %v, note %q", s.Actions.Rename, s.RenameNote)
	}
	p := New(&testEnv{}, State{ID: "rn3", URL: "http://x.test/a.part1.rar", Dir: t.TempDir(), Filename: "a.part1.rar", Status: StatusPaused})
	if err := p.Rename("b.rar"); err == nil {
		t.Fatal("renamed a split archive's part")
	}
	if s := p.Summary(); s.Actions.Rename || s.RenameNote != noRenameSet {
		t.Fatalf("offered %v, note %q", s.Actions.Rename, s.RenameNote)
	}
}
