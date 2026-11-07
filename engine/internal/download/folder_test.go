package download

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io/fs"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/winutil"
)

type zipFile struct {
	name  string
	data  []byte
	store bool // STORED instead of DEFLATE
	flags uint16
}

// manyFiles is n files of size bytes each, under prefix, every third STORED.
// Random data, so DEFLATE can't shrink it and the archive stays as big.
func manyFiles(prefix string, n, size int) []zipFile {
	r := rand.New(rand.NewSource(7))
	var fs []zipFile
	for i := range n {
		b := make([]byte, size)
		r.Read(b)
		fs = append(fs, zipFile{name: fmt.Sprintf("%sdir%d/file%03d.bin", prefix, i%4, i), data: b, store: i%3 == 0})
	}
	return fs
}

func buildZip(t *testing.T, files []zipFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Deflate, Flags: f.flags, Modified: t0}
		if f.store {
			h.Method = zip.Store
		}
		fw, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write(f.data)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// zipServer serves data as big.zip. corrupt, if set, gives the bytes to send
// for a data request (probes get the real ones).
type zipServer struct {
	*httptest.Server
	data    []byte
	corrupt func(start, end int64) []byte
	mu      sync.Mutex
}

func newZipServer(data []byte) *zipServer {
	s := &zipServer{data: data}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := s.data
		rg := r.Header.Get("Range")
		if c := s.corrupt; c != nil && rg != "bytes=0-0" {
			start, end := parseRange(rg, int64(len(s.data)))
			s.mu.Lock()
			body = c(start, end)
			s.mu.Unlock()
		}
		w.Header().Set("Content-Disposition", `attachment; filename="big.zip"`)
		w.Header().Set("ETag", `"z1"`)
		http.ServeContent(w, r, "big.zip", t0, bytes.NewReader(body))
	}))
	return s
}

// parseRange is the [start, end) a "bytes=a-b" header asks for.
func parseRange(h string, size int64) (int64, int64) {
	a, b, _ := strings.Cut(strings.TrimPrefix(h, "bytes="), "-")
	start, _ := strconv.ParseInt(a, 10, 64)
	end := size
	if b != "" {
		e, _ := strconv.ParseInt(b, 10, 64)
		end = e + 1
	}
	return start, end
}

func checkTree(t *testing.T, dir string, files []zipFile, strip string) {
	t.Helper()
	for _, f := range files {
		checkFile(t, filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(f.name, strip))), f.data)
	}
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// watchFreed records, while the download runs, whether the .part ever held
// fewer allocated bytes than had been downloaded: its bytes were freed as it went.
func watchFreed(t *testing.T) *atomic.Bool {
	var freed atomic.Bool
	afterExtract = func(f *os.File, onDisk int64) {
		rs, err := winutil.AllocatedRanges(f)
		if err != nil {
			return
		}
		var n int64
		for _, r := range rs {
			n += r.End - r.Start
		}
		if n+1<<20 < onDisk {
			freed.Store(true)
		}
	}
	t.Cleanup(func() { afterExtract = nil })
	return &freed
}

// With the default on, a download the Probe names big.zip ends as a folder,
// with no .zip and no .part, freeing the archive as it goes.
func TestZipDownloadsAsAFolder(t *testing.T) {
	files := manyFiles("", 48, 512<<10)
	data := buildZip(t, files)
	srv := newZipServer(data)
	defer srv.Close()
	into := t.TempDir()
	freed := watchFreed(t)
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 3*time.Millisecond), iface("wifi", 2, 3*time.Millisecond)}, main: "eth"}
	d := runToEnd(t, env, State{ID: "f1", URL: srv.URL + "/dl", Dir: t.TempDir(), Size: -1, Folder: &Folder{Auto: true, Into: into}})
	s := d.Summary()
	if s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	if s.Folder == nil || !s.Folder.On || s.Folder.Files != 48 || s.Folder.Of != 48 {
		t.Fatalf("folder summary %+v", s.Folder)
	}
	out := filepath.Join(into, "big")
	if d.Path() != out || s.Folder.Out != out {
		t.Fatalf("path %s, out %s, want %s", d.Path(), s.Folder.Out, out)
	}
	checkTree(t, out, files, "")
	filepath.WalkDir(out, func(p string, de fs.DirEntry, err error) error {
		if err == nil && !de.IsDir() {
			checkMarked(t, p, srv.URL+"/dl")
		}
		return nil
	})
	if got := names(t, into); len(got) != 1 || got[0] != "big" {
		t.Fatalf("left in the folder: %v (want only big)", got)
	}
	if !freed.Load() {
		t.Error("the .part was never smaller than what had been downloaded: nothing was freed as it went")
	}
}

// A single top-level folder is the output folder, not nested twice, and a
// taken name is numbered.
func TestOneTopFolderIsntNestedAndATakenNameIsNumbered(t *testing.T) {
	files := manyFiles("big/", 6, 64<<10)
	data := buildZip(t, files)
	srv := newZipServer(data)
	defer srv.Close()
	into := t.TempDir()
	os.Mkdir(filepath.Join(into, "big"), 0o755)
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 0)}, main: "eth"}
	d := runToEnd(t, env, State{ID: "f2", URL: srv.URL + "/dl", Dir: into, Size: -1, Folder: &Folder{On: true, Into: into}})
	if s := d.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	out := filepath.Join(into, "big (1)")
	checkTree(t, out, files, "big/")
	if _, err := os.Stat(filepath.Join(out, "big")); err == nil {
		t.Fatal("the top folder is nested twice")
	}
	if es := names(t, filepath.Join(into, "big")); len(es) != 0 {
		t.Fatalf("wrote into the folder that was there: %v", es)
	}
}

// An archive that can't be downloaded as a folder ends as a normal .zip,
// with the reason in Details.
func TestAnEncryptedArchiveDownloadsAsAZip(t *testing.T) {
	files := manyFiles("", 4, 64<<10)
	files[2].flags = 1 // encrypted
	data := buildZip(t, files)
	srv := newZipServer(data)
	defer srv.Close()
	into := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 0)}, main: "eth"}
	d := runToEnd(t, env, State{ID: "f3", URL: srv.URL + "/dl", Dir: into, Size: -1, Folder: &Folder{On: true, Into: into}})
	s := d.Summary()
	if s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	if s.Folder == nil || s.Folder.On || !strings.Contains(s.Folder.Note, "encrypted") {
		t.Fatalf("folder summary %+v, want off with the reason", s.Folder)
	}
	checkFile(t, filepath.Join(into, "big.zip"), data)
}

// Stopped midway and opened again from its saved state, the download goes on
// without extracting the finished Entries again.
func TestResumeLeavesFinishedEntriesAlone(t *testing.T) {
	files := manyFiles("", 40, 512<<10)
	data := buildZip(t, files)
	srv := newZipServer(data)
	defer srv.Close()
	into := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 4*time.Millisecond)}, main: "eth", finished: make(chan struct{})}
	d := New(env, State{ID: "f4", URL: srv.URL + "/dl", Dir: into, Size: -1, Folder: &Folder{On: true, Into: into}})
	d.Start()
	deadline := time.Now().Add(30 * time.Second)
	for len(d.State().Folder.Done) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("nothing extracted: %+v", d.Summary())
		}
		time.Sleep(20 * time.Millisecond)
	}
	d.Stop(StatusPaused)
	st := d.State()
	if len(st.Folder.Done) >= len(files) {
		t.Fatal("finished before it could be stopped")
	}
	// The user changes a finished file: it is theirs now, and stays as they left it.
	first := filepath.Join(st.Folder.Out, filepath.FromSlash(files[0].name))
	if err := os.WriteFile(first, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	env.ifaces = []Iface{iface("eth", 2, 0)}
	st.Status = StatusQueued
	d2 := runToEnd(t, env, st)
	if s := d2.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkFile(t, first, []byte("mine"))
	checkTree(t, st.Folder.Out, files[1:], "")
}

// corruptOnce serves data with one byte flipped in the first n responses that
// carry byte at.
func corruptOnce(data []byte, at int64, n int) func(start, end int64) []byte {
	var served int
	bad := bytes.Clone(data)
	bad[at] ^= 0xff
	return func(start, end int64) []byte {
		if start <= at && at < end && served < n {
			served++
			return bad
		}
		return data
	}
}

// One connection, so every byte is fetched once unless it is fetched again.
func corruptedRun(t *testing.T, n int) (*Download, []zipFile, string) {
	files := manyFiles("", 8, 256<<10)
	data := buildZip(t, files)
	srv := newZipServer(data)
	t.Cleanup(srv.Close)
	srv.corrupt = corruptOnce(data, int64(len(data)/2), n)
	into := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 0)}, main: "eth", finished: make(chan struct{})}
	d := New(env, State{ID: "f5", URL: srv.URL + "/dl", Dir: into, Size: -1, Folder: &Folder{On: true, Into: into}})
	d.Start()
	deadline := time.Now().Add(60 * time.Second)
	for d.Running() { // a pause doesn't count as finished
		if time.Now().After(deadline) {
			t.Fatalf("download did not stop: %+v", d.Summary())
		}
		time.Sleep(20 * time.Millisecond)
	}
	return d, files, filepath.Join(into, "big")
}

func TestACorruptedChunkIsFetchedAgainOnce(t *testing.T) {
	d, files, out := corruptedRun(t, 1)
	if s := d.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkTree(t, out, files, "")
}

func TestAChunkCorruptedTwicePauses(t *testing.T) {
	d, _, _ := corruptedRun(t, 2)
	s := d.Summary()
	if s.Status != StatusPaused || !strings.Contains(s.Error, ".bin") {
		t.Fatalf("status %s: %q, want paused naming the Entry", s.Status, s.Error)
	}
}

func TestTurningTheModeOnIsRefused(t *testing.T) {
	cases := map[string]State{
		"typed hash": {SHA256: strings.Repeat("a", 64)},
		"freed":      {Folder: &Folder{On: true, Into: "x", Freed: []Range{{0, 10}}}},
		"no ranges":  {Probed: true, RangeOK: false, Size: 10},
		"not a zip":  {Filename: "big.iso"},
		"complete":   {Status: StatusCompleted},
	}
	for name, st := range cases {
		st.ID, st.Filename = "r", orDefault(st.Filename, "big.zip")
		if st.Status == "" {
			st.Status = StatusPaused
		}
		d := New(&testEnv{}, st)
		on := st.Folder != nil && st.Folder.On
		if err := d.SetFolder(!on); err == nil {
			t.Errorf("%s: switching the mode was allowed", name)
		}
	}
	d := New(&testEnv{}, State{ID: "ok", Filename: "big.zip", Status: StatusQueued})
	if err := d.SetFolder(true); err != nil {
		t.Fatal(err)
	}
	if err := d.SetHashes(strings.Repeat("a", 64), ""); err != nil || d.State().Folder.On {
		t.Fatalf("a typed hash didn't turn the mode off (%v)", err)
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// Switched on late, with a folder elsewhere on the same drive: the .part moves
// there, the tail is fetched next, and the download still ends as a folder.
func TestSwitchingOnLateMovesThePartAndEndsAsAFolder(t *testing.T) {
	files := manyFiles("", 32, 512<<10)
	data := buildZip(t, files)
	srv := newZipServer(data)
	defer srv.Close()
	first, into := t.TempDir(), t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 4*time.Millisecond)}, main: "eth", finished: make(chan struct{})}
	d := New(env, State{ID: "f6", URL: srv.URL + "/dl", Dir: first, Size: -1})
	d.Start()
	deadline := time.Now().Add(30 * time.Second)
	for d.Summary().Done < 2<<20 {
		if time.Now().After(deadline) {
			t.Fatalf("not started: %+v", d.Summary())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := d.SetFolderInto(into); err != nil {
		t.Fatal(err)
	}
	if err := d.SetFolder(true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-env.finished:
	case <-time.After(60 * time.Second):
		t.Fatalf("download did not finish: %+v", d.Summary())
	}
	if s := d.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkTree(t, filepath.Join(into, "big"), files, "")
	if got := names(t, into); len(got) != 1 {
		t.Fatalf("left in the folder: %v", got)
	}
	if got := names(t, first); len(got) != 0 {
		t.Fatalf("left where it started: %v", got)
	}
}

// The server's MD5 can't be checked without the whole file: it is skipped,
// with a note.
func TestTheServersMD5IsSkippedWithANote(t *testing.T) {
	files := manyFiles("", 4, 64<<10)
	data := buildZip(t, files)
	wrong := make([]byte, 16)
	srv := gcsServer(t, data, func(string) []byte { return wrong })
	into := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 0)}, main: "eth"}
	d := runToEnd(t, env, State{ID: "f7", URL: srv.URL + "/dl", Dir: into, Filename: "big.zip", Size: -1, Folder: &Folder{On: true, Into: into}})
	s := d.Summary()
	if s.Status != StatusCompleted || !strings.Contains(s.Folder.Note, "checksum wasn't checked") {
		t.Fatalf("status %s %q, note %q", s.Status, s.Error, s.Folder.Note)
	}
	checkTree(t, filepath.Join(into, "big"), files, "")
}

// Removing an unfinished Download as folder with its file deletes the folder and the .part.
func TestDiscardDeletesTheFolderAndThePart(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "big")
	os.MkdirAll(filepath.Join(out, "sub"), 0o755)
	os.WriteFile(filepath.Join(out, "sub", "a.txt"), []byte("a"), 0o644)
	part := filepath.Join(dir, "big.zip"+partSuffix)
	os.WriteFile(part, []byte("zip"), 0o644)
	d := New(&testEnv{}, State{ID: "f8", Dir: dir, Filename: "big.zip", Status: StatusPaused,
		Folder: &Folder{On: true, Into: dir, Out: out, Freed: []Range{{0, 1}}}})
	if d.Path() != out {
		t.Fatalf("path %s, want the folder", d.Path())
	}
	d.Discard(true)
	if got := names(t, dir); len(got) != 0 {
		t.Fatalf("left: %v", got)
	}
}

// Deleting the output folder midway pauses the download instead of extracting
// into nothing.
func TestARemovedFolderPauses(t *testing.T) {
	files := manyFiles("", 40, 512<<10)
	data := buildZip(t, files)
	srv := newZipServer(data)
	defer srv.Close()
	into := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 4*time.Millisecond)}, main: "eth", finished: make(chan struct{})}
	d := New(env, State{ID: "f9", URL: srv.URL + "/dl", Dir: into, Size: -1, Folder: &Folder{On: true, Into: into}})
	d.Start()
	deadline := time.Now().Add(30 * time.Second)
	for len(d.State().Folder.Done) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("nothing extracted: %+v", d.Summary())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := os.RemoveAll(d.State().Folder.Out); err != nil {
		t.Fatal(err)
	}
	for d.Running() {
		if time.Now().After(deadline) {
			t.Fatalf("still running: %+v", d.Summary())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s := d.Summary(); s.Status != StatusPaused || !strings.Contains(s.Error, "folder was removed") {
		t.Fatalf("status %s: %q", s.Status, s.Error)
	}
}
