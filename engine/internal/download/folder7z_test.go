package download

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bodgit/sevenzip"
)

// serve7z serves the archive package's test archive name as big.7z, or
// big.rar for a RAR.
func serve7z(t *testing.T, name string) ([]byte, *httptest.Server) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "archive", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	served := "big" + filepath.Ext(name)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="`+served+`"`)
		w.Header().Set("ETag", `"s1"`)
		http.ServeContent(w, r, served, t0, bytes.NewReader(data))
	}))
	t.Cleanup(srv.Close)
	return data, srv
}

func TestSevenZipDownloadsAsAFolder(t *testing.T) {
	for _, name := range []string{"solid.7z", "nonsolid.7z", "bcj2.7z"} {
		data, srv := serve7z(t, name)
		into := t.TempDir()
		env := &testEnv{ifaces: []Iface{iface("eth", 2, time.Millisecond), iface("wifi", 2, time.Millisecond)}, main: "eth"}
		d := runToEnd(t, env, State{ID: "z7", URL: srv.URL + "/dl", Dir: t.TempDir(), Size: -1, Folder: &Folder{Auto: true, Into: into}})
		s := d.Summary()
		if s.Status != StatusCompleted {
			t.Fatalf("%s: status %s: %s", name, s.Status, s.Error)
		}
		if s.Folder == nil || !s.Folder.On || s.Folder.Files != 4 || s.Folder.Of != 4 {
			t.Fatalf("%s: folder summary %+v", name, s.Folder)
		}
		out := filepath.Join(into, "Game") // the archive's one top folder
		if d.Path() != out {
			t.Fatalf("%s: path %s, want %s", name, d.Path(), out)
		}
		zr, err := sevenzip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			rc, _ := f.Open()
			want, _ := io.ReadAll(rc)
			rc.Close()
			got, err := os.ReadFile(filepath.Join(into, filepath.FromSlash(f.Name)))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("%s: %s wrong (%v)", name, f.Name, err)
			}
		}
		if got := names(t, into); len(got) != 1 || got[0] != "Game" {
			t.Fatalf("%s: left in the folder: %v", name, got)
		}
	}
}

// An encrypted archive waits for its password while it downloads: a wrong
// one is asked for again, the right one extracts it.
func TestAnEncryptedArchiveAsksForItsPassword(t *testing.T) {
	for _, name := range []string{"password.7z", "password-header.7z", "password.rar", "password-header.rar"} {
		_, srv := serve7z(t, name)
		into := t.TempDir()
		env := &testEnv{ifaces: []Iface{iface("eth", 2, 0)}, main: "eth", finished: make(chan struct{})}
		d := New(env, State{ID: "z8", URL: srv.URL + "/dl", Dir: into, Size: -1, Folder: &Folder{On: true, Into: into}})
		if err := d.SetArchivePassword("early"); err == nil {
			t.Fatalf("%s: a password taken before one is asked for", name)
		}
		d.Start()
		waitAsk := func(want string) {
			t.Helper()
			for end := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
				s := d.Summary()
				if s.Folder != nil && s.Folder.Ask == want {
					if !s.Folder.On || s.Status == StatusCompleted || !strings.Contains(s.Note, "password") {
						t.Fatalf("%s: %s, %q, folder %+v", name, s.Status, s.Note, s.Folder)
					}
					return
				}
				if time.Now().After(end) {
					t.Fatalf("%s: never asked (%q): %+v", name, want, s.Folder)
				}
			}
		}
		waitAsk(AskPassword)
		if err := d.SetArchivePassword("wrong"); err != nil {
			t.Fatal(err)
		}
		waitAsk(AskWrong)
		if err := d.SetArchivePassword("secret"); err != nil {
			t.Fatal(err)
		}
		select {
		case <-env.finished:
		case <-time.After(60 * time.Second):
			t.Fatalf("%s: did not finish: %+v", name, d.Summary())
		}
		s := d.Summary()
		if s.Status != StatusCompleted || s.Folder.Files != 1 || d.State().Folder.Password != "" {
			t.Fatalf("%s: %s %s, folder %+v", name, s.Status, s.Error, s.Folder)
		}
		if got := names(t, into); len(got) != 1 {
			t.Fatalf("%s: left in the folder: %v", name, got)
		}
	}
}

// A RAR has no list of files to take a Root from: it is extracted into a
// folder named after it.
func TestRarDownloadsAsAFolder(t *testing.T) {
	want, _ := serve7z(t, "solid.7z")
	zr, err := sevenzip.NewReader(bytes.NewReader(want), int64(len(want)))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"solid.rar", "nonsolid.rar"} {
		_, srv := serve7z(t, name)
		into := t.TempDir()
		env := &testEnv{ifaces: []Iface{iface("eth", 2, time.Millisecond), iface("wifi", 2, time.Millisecond)}, main: "eth"}
		d := runToEnd(t, env, State{ID: "r1", URL: srv.URL + "/dl", Dir: t.TempDir(), Size: -1, Folder: &Folder{Auto: true, Into: into}})
		s := d.Summary()
		if s.Status != StatusCompleted {
			t.Fatalf("%s: status %s: %s", name, s.Status, s.Error)
		}
		if s.Folder == nil || !s.Folder.On || s.Folder.Files != 4 || s.Folder.Of != 4 {
			t.Fatalf("%s: folder summary %+v", name, s.Folder)
		}
		out := filepath.Join(into, "big")
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			rc, _ := f.Open()
			data, _ := io.ReadAll(rc)
			rc.Close()
			got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(f.Name)))
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("%s: %s wrong (%v)", name, f.Name, err)
			}
		}
	}
}

// One volume of a split RAR downloads as a .rar for now.
func TestARarVolumeDownloadsAsARar(t *testing.T) {
	data, srv := serve7z(t, "volume.part1.rar")
	into := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 0)}, main: "eth"}
	d := runToEnd(t, env, State{ID: "r2", URL: srv.URL + "/dl", Dir: into, Size: -1, Folder: &Folder{On: true, Into: into}})
	s := d.Summary()
	if s.Folder == nil || s.Folder.On || !strings.Contains(s.Folder.Note, "split archive") {
		t.Fatalf("folder summary %+v, want off with the reason", s.Folder)
	}
	checkFile(t, filepath.Join(into, "big.rar"), data)
}
