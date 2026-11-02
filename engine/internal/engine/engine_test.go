package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/torrent"
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
	e.settings.MaxParallel = 1
	busy, _ := e.Add(AddRequest{URL: "https://example.com/big.iso"})
	b, _ := e.get(busy)
	b.Start() // the only slot is taken
	defer b.Stop(download.StatusPaused)
	id, err := e.Add(AddRequest{URL: testMagnet})
	if err != nil {
		t.Fatal(err)
	}
	// Learning the files needs no slot: the add step must show them soon.
	s := summaryOf(t, e, id)
	if s.Kind != "torrent" || s.Filename != "debian.iso" || s.Status != torrent.StatusMetadata {
		t.Fatalf("listed as kind %q name %q status %q, want a torrent named debian.iso fetching metadata", s.Kind, s.Filename, s.Status)
	}
	if _, err := e.Call(nil, "chooseFiles", []byte(`{"id":"`+id+`","files":[true]}`)); err == nil {
		t.Fatal("chose files before they were known")
	}
	e.Shutdown()

	e, err = open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	e.schedule()
	j, _ := e.entry(id)
	if s := summaryOf(t, e, id); s.Status != torrent.StatusMetadata || !j.(*torrent.Torrent).Active() {
		t.Fatalf("after restart the torrent is %q (active %v), want fetching metadata again", s.Status, j.(*torrent.Torrent).Active())
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

// seedingTorrent saves a complete torrent that was seeding when the engine
// stopped, with its file in saveDir, and returns its ID.
func seedingTorrent(t *testing.T, engineDir, saveDir string) string {
	t.Helper()
	data := make([]byte, 300<<10)
	rand.Read(data)
	file := filepath.Join(saveDir, "seed.bin")
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	info := metainfo.Info{PieceLength: 64 << 10}
	if err := info.BuildFromFilePath(file); err != nil {
		t.Fatal(err)
	}
	ib, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	mi := metainfo.MetaInfo{InfoBytes: ib}
	m := metainfo.Magnet{InfoHash: mi.HashInfoBytes(), DisplayName: info.Name}
	st := torrent.State{
		ID: "seed1", Magnet: m.String(), Info: ib, Dir: saveDir, Name: info.Name, Size: info.TotalLength(),
		Status: torrent.StatusSeeding, Added: time.Now(), Finished: time.Now(),
	}
	if err := writeJSON(filepath.Join(engineDir, "torrents", st.ID+".json"), st); err != nil {
		t.Fatal(err)
	}
	return st.ID
}

func TestSeedingTorrentKeepsSeedingAfterARestartWithoutASlot(t *testing.T) {
	dir, saveDir := t.TempDir(), t.TempDir()
	seed := seedingTorrent(t, dir, saveDir)
	e, err := open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	e.settings.MaxParallel = 1
	if s := summaryOf(t, e, seed); s.Status != torrent.StatusSeeding {
		t.Fatalf("after restart the seeding torrent is %q", s.Status)
	}
	queued, err := e.Add(AddRequest{URL: "https://example.com/big.iso"})
	if err != nil {
		t.Fatal(err)
	}
	e.schedule()
	q, _ := e.entry(queued)
	defer q.Stop(download.StatusPaused)
	if !q.Running() {
		t.Fatal("the seeding torrent took the only slot; the queued one didn't start")
	}
	j, _ := e.entry(seed)
	waitFor(t, "the restored torrent to seed again", 10*time.Second, func() bool {
		return j.(*torrent.Torrent).Active() && j.Status() == torrent.StatusSeeding
	})
}

func TestRemovingASeedingTorrentKeepsItsFiles(t *testing.T) {
	dir, saveDir := t.TempDir(), t.TempDir()
	seed := seedingTorrent(t, dir, saveDir)
	e, err := open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	e.schedule()
	if err := e.Remove(seed, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(saveDir, "seed.bin")); err != nil {
		t.Fatalf("removing a seeding torrent deleted its file: %v", err)
	}
}

func waitFor(t *testing.T, what string, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestTorrentFileIsFetchedWithTheBrowsersCookie(t *testing.T) {
	file := []byte("d4:infod4:name1:xee")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "sid=1" || r.Header.Get("Referer") != "https://tracker.example/t/1" {
			http.Error(w, "log in first", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/x-bittorrent")
		w.Write(file)
	}))
	defer srv.Close()
	r := AddRequest{URL: srv.URL + "/dl/1", Cookie: "sid=1", Referer: "https://tracker.example/t/1", UserAgent: "test"}
	got, err := fetchTorrentFile(context.Background(), srv.Client(), r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, file) {
		t.Fatalf("fetched %q, want %q", got, file)
	}
	r.Cookie = ""
	if _, err := fetchTorrentFile(context.Background(), srv.Client(), r); err == nil {
		t.Fatal("a refused fetch was taken as a torrent file")
	}
}

func TestAddedTorrentFileGoesStraightToTheChoice(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	data := make([]byte, 100<<10)
	path := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(path, data, 0o644)
	info := metainfo.Info{PieceLength: 64 << 10}
	if err := info.BuildFromFilePath(path); err != nil {
		t.Fatal(err)
	}
	ib, _ := bencode.Marshal(info)
	var buf bytes.Buffer
	(&metainfo.MetaInfo{InfoBytes: ib}).Write(&buf)

	id, err := e.addTorrentFile(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	s := summaryOf(t, e, id)
	if s.Status != torrent.StatusChoosing || len(s.Files) != 1 || s.Files[0].Path != "notes.txt" {
		t.Fatalf("added torrent file is %q with files %+v, want choosing notes.txt", s.Status, s.Files)
	}
}

func TestTorrentLinksAreRecognised(t *testing.T) {
	for url, want := range map[string]bool{
		"https://example.com/ubuntu.iso.torrent":     true,
		"https://example.com/get.php?f=a.TORRENT":    false, // only the path counts
		"https://example.com/Debian.Torrent?key=abc": true,
		"https://example.com/ubuntu.iso":             false,
	} {
		if got := isTorrentURL(url); got != want {
			t.Errorf("isTorrentURL(%q) = %v, want %v", url, got, want)
		}
	}
}

// queuedTorrent adds a torrent whose files are already chosen, so the queue
// starts it like an HTTP download. n makes its info hash unique.
func queuedTorrent(t *testing.T, e *Engine, n int) string {
	t.Helper()
	m := fmt.Sprintf("magnet:?xt=urn:btih:%040x&dn=t%d", n, n)
	tor, err := e.torrents.Add(torrent.State{ID: newID(), Magnet: m, Dir: t.TempDir(), Status: download.StatusQueued, Added: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.downloads[tor.ID()] = tor
	e.order = append(e.order, tor.ID())
	e.mu.Unlock()
	return tor.ID()
}

func running(t *testing.T, e *Engine, id string) bool {
	t.Helper()
	j, err := e.entry(id)
	if err != nil {
		t.Fatal(err)
	}
	return j.Running()
}

func TestQueuedHTTPStartsBeforeAnEarlierQueuedTorrent(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	e.settings.MaxParallel = 1
	tor := queuedTorrent(t, e, 1)
	http, _ := e.Add(AddRequest{URL: "https://example.com/big.iso"})
	e.schedule()
	if !running(t, e, http) || running(t, e, tor) {
		t.Fatalf("http running %v, torrent running %v; want the HTTP download first", running(t, e, http), running(t, e, tor))
	}
}

func TestHTTPTakesTheSlotOfTheLatestDownloadingTorrent(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	e.settings.MaxParallel = 2
	first, second := queuedTorrent(t, e, 1), queuedTorrent(t, e, 2)
	e.schedule()
	if !running(t, e, first) || !running(t, e, second) {
		t.Fatal("both torrents should download while slots are free")
	}

	http, _ := e.Add(AddRequest{URL: "https://example.com/big.iso"})
	e.schedule()
	j, _ := e.entry(second)
	if !running(t, e, http) || !running(t, e, first) || running(t, e, second) || j.Status() != download.StatusQueued {
		t.Fatalf("http %v, first torrent %v, second torrent %v (%q); want the latest torrent bumped back to queued",
			running(t, e, http), running(t, e, first), running(t, e, second), j.Status())
	}

	h, _ := e.entry(http)
	h.Stop(download.StatusPaused)
	e.schedule()
	if !running(t, e, second) {
		t.Fatal("the bumped torrent didn't resume when the slot freed")
	}
}
