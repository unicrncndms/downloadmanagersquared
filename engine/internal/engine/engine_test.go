package engine

import (
	"os"
	"path/filepath"
	"testing"

	"downloadmanagersquared/engine/internal/download"
)

const testMagnet = "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=debian.iso"

func summaryOf(t *testing.T, e *Engine, id string) download.Summary {
	t.Helper()
	e.updateSnapshot(0)
	for _, s := range e.Snapshot().Downloads {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("download %s not in the list", id)
	return download.Summary{}
}

func TestAddedTorrentIsListedAndSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	e, err := open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := e.Add(AddRequest{URL: testMagnet})
	if err != nil {
		t.Fatal(err)
	}
	s := summaryOf(t, e, id)
	if s.Kind != "torrent" || s.Filename != "debian.iso" || s.Status != download.StatusQueued {
		t.Fatalf("listed as kind %q name %q status %q, want a queued torrent named debian.iso", s.Kind, s.Filename, s.Status)
	}
	e.Shutdown()

	e, err = open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	s = summaryOf(t, e, id)
	if s.Kind != "torrent" || s.Filename != "debian.iso" || s.Status != download.StatusQueued {
		t.Fatalf("after restart: kind %q name %q status %q, want the same queued torrent", s.Kind, s.Filename, s.Status)
	}
}

func TestRemovingANamelessTorrentKeepsTheDownloadFolder(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	saveDir := t.TempDir()
	e.settings.SaveDir = saveDir
	other := filepath.Join(saveDir, "someone else's file.txt")
	if err := os.WriteFile(other, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No dn: the torrent's name is unknown until peers send its metadata.
	id, err := e.Add(AddRequest{URL: "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Remove(id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("removing the torrent deleted other files in the download folder: %v", err)
	}
}
