package download

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bodgit/sevenzip"

	"downloadmanagersquared/engine/internal/archive"
)

// serveParts serves the archive package's split test archive name.001… as
// big.7z.001…, each at /<n>.
func serveParts(t *testing.T, name string) (whole []byte, srv *httptest.Server) {
	t.Helper()
	var src, served []string
	for i := 1; ; i++ {
		f := fmt.Sprintf("%s.%03d", name, i)
		if _, err := os.Stat(filepath.Join("..", "archive", "testdata", f)); err != nil {
			break
		}
		src, served = append(src, f), append(served, fmt.Sprintf("big.7z.%03d", i))
	}
	return serveFiles(t, src, served)
}

// serveFiles serves the archive package's test files src as served, the
// n-th at /<n> from 1.
func serveFiles(t *testing.T, src, served []string) (whole []byte, srv *httptest.Server) {
	t.Helper()
	var parts [][]byte
	for _, f := range src {
		b, err := os.ReadFile(filepath.Join("..", "archive", "testdata", f))
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, b)
		whole = append(whole, b...)
	}
	return whole, serveBytes(t, parts, served)
}

// serveBytes serves parts under the names served, the n-th at /<n> from 1.
func serveBytes(t *testing.T, parts [][]byte, served []string) (srv *httptest.Server) {
	t.Helper()
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n int
		fmt.Sscanf(r.URL.Path, "/%d", &n)
		if n < 1 || n > len(parts) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="`+served[n-1]+`"`)
		w.Header().Set("ETag", fmt.Sprintf(`"p%d"`, n))
		http.ServeContent(w, r, served[n-1], t0, bytes.NewReader(parts[n-1]))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// setEnv is a testEnv for several downloads: Finished doesn't close anything.
// It notes the most Entries part 1 was saved with as done while some .part
// was still there.
type setEnv struct {
	testEnv
	saveMu        sync.Mutex
	doneWithAPart int
}

func (e *setEnv) Finished(*Download) {}

func (e *setEnv) Save(d *Download) error {
	d.mu.Lock()
	dir, done := d.st.Dir, 0
	if f := d.st.Folder; f != nil {
		done = len(f.Done)
	}
	d.mu.Unlock()
	if parts, _ := filepath.Glob(filepath.Join(dir, "*"+partSuffix)); d.ID() == "p1" && len(parts) > 0 {
		e.saveMu.Lock()
		e.doneWithAPart = max(e.doneWithAPart, done)
		e.saveMu.Unlock()
	}
	return nil
}

// part is a download of part n, as folder into `into`.
func part(env Env, srv *httptest.Server, n int, into string) *Download {
	return New(env, State{ID: fmt.Sprintf("p%d", n), URL: fmt.Sprintf("%s/%d", srv.URL, n), Dir: into, Size: -1,
		Folder: &Folder{On: true, Into: into}})
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(30 * time.Second); !ok(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("never: %s", what)
		}
	}
}

func completed(ds ...*Download) func() bool {
	return func() bool {
		for _, d := range ds {
			if d.Summary().Status != StatusCompleted {
				return false
			}
		}
		return true
	}
}

func checkSet(t *testing.T, whole []byte, out string) {
	t.Helper()
	zr, err := sevenzip.NewReader(bytes.NewReader(whole), int64(len(whole)))
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
		rel := strings.TrimPrefix(f.Name, "Game/") // the Root is the output folder
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(rel)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s wrong (%v)", f.Name, err)
		}
	}
}

// The parts are added in any order and download one at a time, as with one
// download at once; the set extracts them into one folder and leaves no part.
func TestASplit7zExtractsAcrossItsPartsDownloads(t *testing.T) {
	for _, name := range []string{"split-solid.7z", "split-nonsolid.7z"} {
		whole, srv := serveParts(t, name)
		into := t.TempDir()
		env := &setEnv{testEnv: testEnv{ifaces: []Iface{iface("eth", 2, 0)}, main: "eth"}}
		p := map[int]*Download{}
		for _, n := range []int{3, 1, 2} {
			p[n] = part(env, srv, n, into)
		}
		s := NewSet("k", "big.7z")
		s.SetMembers(p)
		s.Start()
		for _, n := range []int{3, 1, 2} {
			p[n].Start()
			waitUntil(t, fmt.Sprintf("%s: part %d completed", name, n), completed(p[n]))
		}
		waitUntil(t, name+": set finished", func() bool { v := s.Summary(); return v != nil && v.Finished })
		s.Stop()
		out := filepath.Join(into, "Game")
		checkSet(t, whole, out)
		if got := names(t, into); len(got) != 1 || got[0] != "Game" {
			t.Fatalf("%s: left in the folder: %v", name, got)
		}
		// Part 1's record of the Entries done was on disk before the last .part went.
		p[1].mu.Lock()
		done := len(p[1].st.Folder.Done)
		p[1].mu.Unlock()
		if env.saveMu.Lock(); env.doneWithAPart != done {
			t.Fatalf("%s: part 1 saved with %d of its %d Entries done before the parts went", name, env.doneWithAPart, done)
		}
		env.saveMu.Unlock()
		if v := s.Summary(); v.Files != 4 || v.Of != 4 || v.Parts != 3 || len(v.Missing) > 0 {
			t.Fatalf("%s: set %+v", name, v)
		}
		if f := p[2].Summary().Folder; f == nil || f.Set == nil || !f.Set.Finished {
			t.Fatalf("%s: part 2's folder %+v", name, f)
		}
		// Its progress ends at all of the archive; nothing is left to wait for.
		if v := s.Summary(); v.Extracted != int64(len(whole)) || v.Total != int64(len(whole)) || v.Phase != "" || v.ETA != 0 {
			t.Fatalf("%s: progress %d of %d, phase %q, %v s left; want all %d and done", name, v.Extracted, v.Total, v.Phase, v.ETA, len(whole))
		}
		// Both are read front to back only: the solid one is one stream, and
		// the other's largest file is longer than a part.
		if !s.inOrder.Load() {
			t.Fatalf("%s: not sequential", name)
		}
	}
}

func TestASplit7zWaitsForAMissingPartAndSaysWhich(t *testing.T) {
	whole, srv := serveParts(t, "split-nonsolid.7z")
	into := t.TempDir()
	env := &setEnv{testEnv: testEnv{ifaces: []Iface{iface("eth", 2, 0)}, main: "eth"}}
	p1, p2, p3 := part(env, srv, 1, into), part(env, srv, 2, into), part(env, srv, 3, into)
	s := NewSet("k", "big.7z")
	s.SetMembers(map[int]*Download{1: p1, 3: p3})
	s.Start()
	defer s.Stop()
	p1.Start()
	p3.Start()
	waitUntil(t, "parts 1 and 3 completed", completed(p1, p3))
	waitUntil(t, "part 2 missing", func() bool {
		v := s.Summary()
		return v != nil && v.Note == "Part 2 missing" && len(v.Missing) == 1 && v.Missing[0] == 2
	})
	if n := p1.Summary().Note; n != "Part 2 missing" {
		t.Fatalf("part 1 says %q", n)
	}
	if v := s.Summary(); v.Finished {
		t.Fatal("finished without part 2")
	}
	s.SetMembers(map[int]*Download{1: p1, 2: p2, 3: p3})
	p2.Start()
	waitUntil(t, "set finished", func() bool { v := s.Summary(); return v != nil && v.Finished })
	waitUntil(t, "part 2 completed", completed(p2))
	checkSet(t, whole, filepath.Join(into, "Game"))
	if got := names(t, into); len(got) != 1 {
		t.Fatalf("left in the folder: %v", got)
	}
}

// A set stopped partway through a block it streams (a pause, a restart)
// starts that block over: the bytes freed of it are fetched again, from a
// part already completed too.
func TestAStoppedSetFetchesTheFreedBytesOfItsBlockAgain(t *testing.T) {
	was, wasStep := archive.LargeEntry, archive.FreeStep
	archive.LargeEntry, archive.FreeStep = 1<<10, 16<<10
	t.Cleanup(func() { archive.LargeEntry, archive.FreeStep = was, wasStep })

	whole, srv := serveParts(t, "split-solid.7z") // one block across all three parts
	into := t.TempDir()
	env := &setEnv{testEnv: testEnv{ifaces: []Iface{iface("eth", 2, 0)}, main: "eth"}}
	p := map[int]*Download{1: part(env, srv, 1, into), 2: part(env, srv, 2, into), 3: part(env, srv, 3, into)}
	p[3].Start()
	p[1].Start()
	waitUntil(t, "parts 1 and 3 completed", completed(p[1], p[3]))
	s := NewSet("k", "big.7z")
	s.SetMembers(p)
	s.Start()
	waitUntil(t, "part 1 freed behind the decoder", func() bool {
		f := p[1].State().Folder
		return f != nil && len(f.Freed) > 0
	})
	s.Stop()
	s.SetMembers(nil)

	s = NewSet("k", "big.7z")
	s.SetMembers(p)
	s.Start()
	defer s.Stop()
	waitUntil(t, "part 1 to fetch again", func() bool { return p[1].Summary().Status == StatusQueued })
	p[1].Start()
	p[2].Start()
	waitUntil(t, "set finished", func() bool { v := s.Summary(); return v != nil && v.Finished })
	waitUntil(t, "every part completed", completed(p[1], p[2], p[3]))
	checkSet(t, whole, filepath.Join(into, "Game"))
	if got := names(t, into); len(got) != 1 {
		t.Fatalf("left in the folder: %v", got)
	}
}

// A RAR in volumes extracts across its parts' downloads, with either naming:
// x.part1.rar… (RAR5) and x.rar, x.r00… (RAR4), parts 2 and 3 added late.
func TestARarSetExtractsAcrossItsVolumesDownloads(t *testing.T) {
	seven, err := os.ReadFile(filepath.Join("..", "archive", "testdata", "solid.7z"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ src, served string }{
		{"volume.part%d.rar", "big.part%d.rar"},
		{"rar4-volume.part%d.rar", ""},
	} {
		var src, served []string
		for i := 1; i <= 3; i++ {
			src = append(src, fmt.Sprintf(c.src, i))
			if c.served != "" {
				served = append(served, fmt.Sprintf(c.served, i))
			}
		}
		if c.served == "" {
			served = []string{"big.rar", "big.r00", "big.r01"}
		}
		_, srv := serveFiles(t, src, served)
		into := t.TempDir()
		env := &setEnv{testEnv: testEnv{ifaces: []Iface{iface("eth", 2, 0)}, main: "eth"}}
		p := map[int]*Download{1: part(env, srv, 1, into), 2: part(env, srv, 2, into), 3: part(env, srv, 3, into)}
		s := NewSet("k", "big.rar")
		s.SetMembers(map[int]*Download{1: p[1]})
		s.Start()
		p[1].Start()
		waitUntil(t, c.src+": waits for part 2", func() bool { v := s.Summary(); return v != nil && v.Note == "Part 2 missing" })
		s.SetMembers(p)
		p[2].Start()
		p[3].Start()
		waitUntil(t, c.src+": set finished", func() bool { v := s.Summary(); return v != nil && v.Finished })
		waitUntil(t, c.src+": every part completed", completed(p[1], p[2], p[3]))
		s.Stop()
		checkSet(t, seven, filepath.Join(into, "big", "Game"))
		if got := names(t, into); len(got) != 1 || got[0] != "big" {
			t.Fatalf("%s: left in the folder: %v", c.src, got)
		}
		if v := s.Summary(); v.Files != 4 || v.Of != 4 || v.Parts != 3 {
			t.Fatalf("%s: set %+v", c.src, v)
		}
	}
}

func TestWhichNamesAreSetParts(t *testing.T) {
	for name, want := range map[string]int{
		"x.7z.001": 1, "x.7z.012": 12, "x.part1.rar": 1, "x.part03.rar": 3, "x.r00": 2, "x.r11": 13,
		"x.z01": 1, "x.z12": 12, "x.zip.003": 3,
		"x.7z": 0, "x.rar": 0, "x.rar.001": 0, "x.zip": 0,
	} {
		if _, part, ok := Splittable(name); part != want || ok != (want > 0) {
			t.Errorf("%s: part %d, %v; want %d", name, part, ok, want)
		}
	}
	d := New(&testEnv{}, State{ID: "a", Filename: "x.rar", Dir: `C:\D`})
	if key, set, part, lone := setOf(d); key == "" || set != "x.rar" || part != 1 || !lone {
		t.Fatalf("x.rar: %q %q %d %v", key, set, part, lone)
	}
	r00 := New(&testEnv{}, State{ID: "b", Filename: "x.r00", Dir: `C:\D`})
	if key, _, part, lone := setOf(r00); part != 2 || lone || key != func() string { k, _, _, _ := setOf(d); return k }() {
		t.Fatalf("x.r00: %q %d %v", key, part, lone)
	}
}

// A split ZIP extracts across its parts' downloads: x.z01, x.z02, x.zip
// (WinRAR), the .zip keyed as the last part until it says how many there
// are, and x.zip.001… (7-Zip), a ZIP cut in pieces.
func TestASplitZipExtractsAcrossItsPartsDownloads(t *testing.T) {
	seven, err := os.ReadFile(filepath.Join("..", "archive", "testdata", "solid.7z"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		src, served []string
		keys        []int
	}{
		{[]string{"splitwr.z01", "splitwr.z02", "splitwr.zip"}, []string{"big.z01", "big.z02", "big.zip"}, []int{1, 2, archive.LastPart}},
		{[]string{"split7.zip.001", "split7.zip.002", "split7.zip.003"}, []string{"big.zip.001", "big.zip.002", "big.zip.003"}, []int{1, 2, 3}},
	} {
		_, srv := serveFiles(t, c.src, c.served)
		into := t.TempDir()
		env := &setEnv{testEnv: testEnv{ifaces: []Iface{iface("eth", 2, 0)}, main: "eth"}}
		p := map[int]*Download{}
		for i, k := range c.keys {
			p[k] = part(env, srv, i+1, into)
		}
		s := NewSet("k", "big.zip")
		s.SetMembers(p)
		s.Start()
		for _, d := range p {
			d.Start()
		}
		waitUntil(t, c.served[0]+": set finished", func() bool { v := s.Summary(); return v != nil && v.Finished })
		for _, d := range p {
			waitUntil(t, c.served[0]+": parts completed", completed(d))
		}
		s.Stop()
		checkSet(t, seven, filepath.Join(into, "Game"))
		if got := names(t, into); len(got) != 1 || got[0] != "Game" {
			t.Fatalf("%s: left in the folder: %v", c.served[0], got)
		}
		if v := s.Summary(); v.Files != 4 || v.Of != 4 || v.Parts != 3 || len(v.Missing) > 0 {
			t.Fatalf("%s: set %+v", c.served[0], v)
		}
	}
}

// An encrypted archive in parts asks for its password on part 1, as one
// download does: a wrong one asks again, the right one extracts the set.
func TestAnEncryptedSplit7zAsksForItsPassword(t *testing.T) {
	for _, name := range []string{"password.7z", "password-header.7z"} {
		whole, err := os.ReadFile(filepath.Join("..", "archive", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		// A 7z cut in three, as 7-Zip splits one.
		size := (len(whole) + 2) / 3
		parts := [][]byte{whole[:size], whole[size : 2*size], whole[2*size:]}
		srv := serveBytes(t, parts, []string{"big.7z.001", "big.7z.002", "big.7z.003"})
		into := t.TempDir()
		env := &setEnv{testEnv: testEnv{ifaces: []Iface{iface("eth", 2, 0)}, main: "eth"}}
		p := map[int]*Download{}
		for n := 1; n <= 3; n++ {
			p[n] = part(env, srv, n, into)
		}
		s := NewSet("k", "big.7z")
		s.SetMembers(p)
		s.Start()
		for n := 1; n <= 3; n++ {
			p[n].Start()
		}
		asks := func(want string) {
			t.Helper()
			waitUntil(t, name+": asks "+want, func() bool { return p[1].Summary().Folder.Ask == want })
		}
		asks(AskPassword)
		if err := p[1].SetArchivePassword("wrong"); err != nil {
			t.Fatal(err)
		}
		asks(AskWrong)
		if err := p[1].SetArchivePassword("secret"); err != nil {
			t.Fatal(err)
		}
		waitUntil(t, name+": set finished", func() bool { v := s.Summary(); return v != nil && v.Finished })
		s.Stop()
		if v := s.Summary(); v.Files != 1 || p[1].State().Folder.Password != "" {
			t.Fatalf("%s: set %+v, password kept: %v", name, v, p[1].State().Folder.Password != "")
		}
	}
}

// A 7z's list of files is at the end of its last part: once part 1 has said
// how the parts lie, the Set wants that part first, until it has read the list.
func TestASplit7zWantsItsLastPartsTailFirst(t *testing.T) {
	_, srv := serveParts(t, "split-nonsolid.7z")
	into := t.TempDir()
	env := &setEnv{testEnv: testEnv{ifaces: []Iface{iface("eth", 2, 0)}, main: "eth"}}
	p := map[int]*Download{}
	for n := 1; n <= 3; n++ {
		p[n] = part(env, srv, n, into)
	}
	s := NewSet("k", "big.7z")
	s.SetMembers(p)
	s.Start()
	defer s.Stop()
	if s.tail.Load() != nil {
		t.Fatal("a tail wanted before part 1 has said how the parts lie")
	}
	p[1].Start()
	waitUntil(t, "the set wants part 3's tail", func() bool { return s.tail.Load() == p[3] })
	p[3].Start()
	waitUntil(t, "the list of files is read: no part's tail is wanted", func() bool { return s.tail.Load() == nil })
}

// A block that comes out wrong is fetched again once; wrong a second time, the
// parts it is in pause and say so, as one archive's download does. Resumed
// with the right bytes, the set finishes.
func TestASetsBlockWrongTwicePausesItsParts(t *testing.T) {
	var good, wrong [][]byte
	var whole []byte
	var served []string
	for i := 1; i <= 3; i++ {
		b, err := os.ReadFile(filepath.Join("..", "archive", "testdata", fmt.Sprintf("split-nonsolid.7z.%03d", i)))
		if err != nil {
			t.Fatal(err)
		}
		good, wrong, whole = append(good, b), append(wrong, bytes.Clone(b)), append(whole, b...)
		served = append(served, fmt.Sprintf("big.7z.%03d", i))
	}
	wrong[1][len(wrong[1])/2] ^= 0xff
	var bad atomic.Bool
	bad.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n int
		fmt.Sscanf(r.URL.Path, "/%d", &n)
		parts := good
		if bad.Load() {
			parts = wrong
		}
		w.Header().Set("Content-Disposition", `attachment; filename="`+served[n-1]+`"`)
		w.Header().Set("ETag", fmt.Sprintf(`"p%d"`, n))
		http.ServeContent(w, r, served[n-1], t0, bytes.NewReader(parts[n-1]))
	}))
	t.Cleanup(srv.Close)

	into := t.TempDir()
	env := &setEnv{testEnv: testEnv{ifaces: []Iface{iface("eth", 2, 0)}, main: "eth"}}
	p := map[int]*Download{1: part(env, srv, 1, into), 2: part(env, srv, 2, into), 3: part(env, srv, 3, into)}
	s := NewSet("k", "big.7z")
	s.SetMembers(p)
	s.Start()
	defer s.Stop()
	// No queue here: a part put back to be fetched again is started by hand.
	startQueued := func() {
		for _, d := range p {
			if d.Summary().Status == StatusQueued {
				d.Start()
			}
		}
	}
	for _, d := range p {
		d.Start()
	}
	waitUntil(t, "part 2 paused", func() bool {
		startQueued()
		// pauseWith says why just after it stops the part.
		s := p[2].Summary()
		return s.Status == StatusPaused && s.Error != ""
	})
	if e := p[2].Summary().Error; !strings.Contains(e, "came out wrong twice") {
		t.Fatalf("part 2 paused with %q, want the Entry wrong twice", e)
	}
	if v := s.Summary(); v != nil && v.Finished {
		t.Fatal("the set finished with a wrong block")
	}

	bad.Store(false)
	for _, d := range p {
		if d.Summary().Status == StatusPaused {
			d.Start()
		}
	}
	waitUntil(t, "set finished", func() bool {
		startQueued()
		v := s.Summary()
		return v != nil && v.Finished
	})
	checkSet(t, whole, filepath.Join(into, "Game"))
}

// A RAR in volumes stopped partway through (an engine restart) has its front
// freed, where its headers are: the next run fetches the freed bytes again and
// extracts it from the start, instead of taking it for something else.
func TestARarSetStoppedAfterFreeingItsFrontStartsOver(t *testing.T) {
	was, wasStep := archive.LargeEntry, archive.FreeStep
	archive.LargeEntry, archive.FreeStep = 1<<10, 4<<10
	t.Cleanup(func() { archive.LargeEntry, archive.FreeStep = was, wasStep })

	seven, err := os.ReadFile(filepath.Join("..", "archive", "testdata", "solid.7z"))
	if err != nil {
		t.Fatal(err)
	}
	var src, served []string
	for i := 1; i <= 3; i++ {
		src, served = append(src, fmt.Sprintf("volume.part%d.rar", i)), append(served, fmt.Sprintf("big.part%d.rar", i))
	}
	_, srv := serveFiles(t, src, served)
	into := t.TempDir()
	env := &setEnv{testEnv: testEnv{ifaces: []Iface{iface("eth", 2, 0)}, main: "eth"}}
	p := map[int]*Download{1: part(env, srv, 1, into), 2: part(env, srv, 2, into), 3: part(env, srv, 3, into)}
	p[1].Start()
	waitUntil(t, "part 1 completed", completed(p[1]))
	s := NewSet("k", "big.rar")
	s.SetMembers(p)
	s.Start()
	waitUntil(t, "part 1's front freed behind the decoder", func() bool {
		f := p[1].State().Folder
		return f != nil && len(f.Freed) > 0 && f.Freed[0].Start == 0
	})
	s.Stop()
	s.SetMembers(nil)
	if v := s.Summary(); v != nil && v.Finished {
		t.Skip("extracted before it could be stopped")
	}

	s = NewSet("k", "big.rar")
	s.SetMembers(p)
	s.Start()
	defer s.Stop()
	waitUntil(t, "set finished", func() bool {
		for _, d := range p {
			if st := d.Summary().Status; st == "" || st == StatusQueued || st == StatusPaused {
				d.Start()
			}
		}
		v := s.Summary()
		return v != nil && v.Finished
	})
	if f := p[1].State().Folder; f == nil || !f.On {
		t.Fatalf("part 1's folder turned off: %+v", f)
	}
	checkSet(t, seven, filepath.Join(into, "big", "Game"))
}
