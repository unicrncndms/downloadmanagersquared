package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCleanHosts(t *testing.T) {
	got := cleanHosts([]string{" HTTPS://www.Bank.example/files?x=1 ", "bank.example", "a.b:8080", "", "."})
	if want := []string{"bank.example", "a.b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if cleanHosts(nil) == nil {
		t.Fatal("nil, want an empty list")
	}
}

func TestCheckSaveDir(t *testing.T) {
	dir := t.TempDir()
	if err := checkSaveDir(dir); err != nil {
		t.Fatal(err)
	}
	if checkSaveDir(filepath.Join(dir, "nope")) == nil || checkSaveDir("") == nil {
		t.Fatal("a missing or empty folder passed")
	}
	f := filepath.Join(dir, "file")
	os.WriteFile(f, nil, 0o600)
	if checkSaveDir(f) == nil {
		t.Fatal("a file passed as a folder")
	}
}

func TestRestoredKeepsConnections(t *testing.T) {
	s := defaultSettings()
	s.Interfaces["a"] = &IfaceSettings{Connections: 8}
	s.MainInterface, s.TorrentPort, s.SeedRatio, s.NeverCapture, s.Theme = "a", 23456, 5, []string{"x.y"}, "dark"
	r := s.restored()
	if r.Interfaces["a"] == nil || r.MainInterface != "a" || r.TorrentPort != 23456 {
		t.Fatal("connections or port lost")
	}
	if r.SeedRatio != 1 || r.NeverCapture != nil || r.Theme != "" {
		t.Fatalf("not reset: %+v", r)
	}
}
