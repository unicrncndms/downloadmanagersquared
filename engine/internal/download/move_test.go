package download

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/winutil"
)

// otherDrive makes every move one between drives, for the length of a test.
func otherDrive(t *testing.T) {
	t.Helper()
	was := sameDrive
	sameDrive = func(a, b string) bool { return false }
	t.Cleanup(func() { sameDrive = was })
}

// startUntil starts a Download as folder of data into into and waits until ok.
func startUntil(t *testing.T, env *testEnv, st State, ok func(*Download) bool) *Download {
	t.Helper()
	env.finished = make(chan struct{})
	d := New(env, st)
	t.Cleanup(d.dropParked) // a test that ends paused leaves its .part open otherwise
	d.Start()
	deadline := time.Now().Add(30 * time.Second)
	for !ok(d) {
		if time.Now().After(deadline) {
			t.Fatalf("never got there: %+v", d.Summary())
		}
		time.Sleep(5 * time.Millisecond)
	}
	return d
}

func waitCompleted(t *testing.T, env *testEnv, d *Download) {
	t.Helper()
	select {
	case <-env.finished:
	case <-time.After(60 * time.Second):
		t.Fatalf("download did not finish: %+v", d.Summary())
	}
	if s := d.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
}

func someExtracted(d *Download) bool {
	f := d.State().Folder
	return f != nil && len(f.Done) > 0 && len(f.Freed) > 0
}

// Moved on the same drive while it downloads, the folder and the .part are
// renamed into the new folder, and the download ends correct there.
func TestASameDriveMoveMidDownloadEndsCorrect(t *testing.T) {
	files := manyFiles("", 40, 256<<10)
	data := buildZip(t, files)
	srv := newZipServer(data)
	defer srv.Close()
	root := t.TempDir()
	from, to := filepath.Join(root, "a"), filepath.Join(root, "b")
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 5*time.Millisecond)}, main: "eth"}
	d := startUntil(t, env, State{ID: "m1", URL: srv.URL + "/dl", Dir: from, Size: -1, Folder: &Folder{On: true, Into: from}}, someExtracted)

	if err := d.SetFolderInto(to); err != nil {
		t.Fatal(err)
	}
	waitCompleted(t, env, d)
	f := d.State().Folder
	if f.Into != to || f.Out != filepath.Join(to, "big") || f.Moving != "" || f.MoveError != "" {
		t.Fatalf("folder after the move: %+v", f)
	}
	checkTree(t, f.Out, files, "")
	if left := names(t, from); len(left) != 0 {
		t.Fatalf("left behind in the old folder: %v", left)
	}
	if left := leftoverParts(t, to); len(left) > 0 {
		t.Fatalf("left %v", left)
	}
}

// A file in the folder held open by another program: nothing moves, and the
// download says why.
func TestAMoveWithAFileHeldOpenChangesNothing(t *testing.T) {
	files := manyFiles("", 40, 256<<10)
	data := buildZip(t, files)
	srv := newZipServer(data)
	defer srv.Close()
	root := t.TempDir()
	from, to := filepath.Join(root, "a"), filepath.Join(root, "b")
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 5*time.Millisecond)}, main: "eth"}
	d := startUntil(t, env, State{ID: "m2", URL: srv.URL + "/dl", Dir: from, Size: -1, Folder: &Folder{On: true, Into: from}}, someExtracted)
	d.Stop(StatusPaused)
	before := d.State()
	out := before.Folder.Out
	held, err := os.Open(filepath.Join(out, filepath.FromSlash(files[0].name)))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	err = d.SetFolderInto(to)
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("moving with a file open: %v", err)
	}
	after := d.State()
	f := after.Folder
	if f.Into != from || f.Out != out || f.Moving != "" || !strings.Contains(f.MoveError, "Couldn't move: a file in it is in use") {
		t.Fatalf("folder after the refused move: %+v", f)
	}
	if after.Dir != before.Dir || after.Filename != before.Filename || !exists(filepath.Join(after.Dir, after.Filename+partSuffix)) {
		t.Fatalf("the .part moved: %s/%s", after.Dir, after.Filename)
	}
	if exists(to) && len(names(t, to)) > 0 {
		t.Fatalf("something arrived at the target: %v", names(t, to))
	}
	if s := d.Summary(); s.Folder == nil || !strings.Contains(s.Folder.MoveError, "in use") {
		t.Fatalf("Details doesn't say why: %+v", s.Folder)
	}

	held.Close()
	after.Status = StatusQueued
	d2 := runToEnd(t, env, after)
	if s := d2.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkTree(t, out, files, "")
}

// Moved to another drive while it downloads: it shows Moving…, copies the
// folder and only the .part's allocated bytes, deletes the old copies and
// ends correct.
func TestACrossDriveMoveCopiesOnlyAllocatedRangesAndEndsCorrect(t *testing.T) {
	otherDrive(t)
	files := manyFiles("", 40, 256<<10)
	data := buildZip(t, files)
	srv := newZipServer(data)
	defer srv.Close()
	root := t.TempDir()
	from, to := filepath.Join(root, "a"), filepath.Join(root, "b")
	var sawMoving atomic.Bool
	var copied, allocated atomic.Int64
	var d *Download
	duringMove = func(oldPart, newPart string) {
		if s := d.Summary(); s.Label == "Moving…" {
			sawMoving.Store(true)
		}
		allocated.Store(allocatedBytes(t, oldPart))
		copied.Store(allocatedBytes(t, newPart))
	}
	t.Cleanup(func() { duringMove = nil })
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 5*time.Millisecond)}, main: "eth"}
	d = startUntil(t, env, State{ID: "m3", URL: srv.URL + "/dl", Dir: from, Size: -1, Folder: &Folder{On: true, Into: from}}, someExtracted)

	if err := d.SetFolderInto(to); err != nil {
		t.Fatal(err)
	}
	waitCompleted(t, env, d)
	if !sawMoving.Load() {
		t.Fatal("never showed Moving…")
	}
	if a, c := allocated.Load(), copied.Load(); a <= 0 || c != a || c >= int64(len(data)) {
		t.Fatalf("copied %d bytes of a .part with %d allocated (archive %d)", c, a, len(data))
	}
	f := d.State().Folder
	if f.Into != to || f.Out != filepath.Join(to, "big") || f.Moving != "" || f.MoveError != "" || f.MoveOut != "" {
		t.Fatalf("folder after the move: %+v", f)
	}
	checkTree(t, f.Out, files, "")
	if left := names(t, from); len(left) != 0 {
		t.Fatalf("left behind in the old folder: %v", left)
	}
}

func allocatedBytes(t *testing.T, path string) int64 {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return allocatedIn(f, 0, 1<<62)
}

// copySparse copies only the allocated ranges, and the copy reads the same.
func TestCopySparseCopiesOnlyAllocatedRanges(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	winutil.SetSparse(f, true)
	f.Truncate(64 << 20)
	f.WriteAt(bytes.Repeat([]byte{1}, 1<<20), 3<<20)
	f.WriteAt(bytes.Repeat([]byte{2}, 2<<20), 40<<20)
	f.Close()
	dst := filepath.Join(dir, "b")
	if err := copySparse(t.Context(), src, dst); err != nil {
		t.Fatal(err)
	}
	if a, b := allocatedBytes(t, src), allocatedBytes(t, dst); b != a || b > 4<<20 {
		t.Fatalf("copy has %d bytes allocated, the original %d", b, a)
	}
	x, _ := os.ReadFile(src)
	y, _ := os.ReadFile(dst)
	if !bytes.Equal(x, y) {
		t.Fatal("the copy reads differently")
	}
}

// Moving while a large Entry is under way re-downloads its freed bytes, and
// Details says how much before the move.
func TestTheRedownloadWarningShowsTheFreedBytesOfTheEntryUnderWay(t *testing.T) {
	lowerLarge(t, 1<<20, 1<<20)
	_, data, e := largeZip(t, 32<<20)
	srv := newZipServer(data)
	defer srv.Close()
	into := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 4*time.Millisecond)}, main: "eth"}
	d := startUntil(t, env, State{ID: "m4", URL: srv.URL + "/dl", Dir: into, Size: -1, Folder: &Folder{On: true, Into: into}},
		func(d *Download) bool { return freedIn(d.State().Folder.Freed, e) >= 4<<20 })
	defer d.Stop(StatusPaused)

	d.mu.Lock()
	got, want := d.folderSummaryLocked().Refetch, freedIn(d.st.Folder.Freed, e)
	d.mu.Unlock()
	if got != want || got == 0 {
		t.Fatalf("warns of %d bytes, want %d (freed of the large Entry)", got, want)
	}
	s := Present(Summary{Status: StatusRunning, Probed: true, RangeOK: true, Folder: &FolderSummary{On: true, Into: into, Out: "x", Refetch: 3435973837}})
	if s.Folder.MoveWarning != "Moving now re-downloads about 3.2 GiB" {
		t.Fatalf("warning %q", s.Folder.MoveWarning)
	}
	if Present(Summary{Status: StatusRunning, Folder: &FolderSummary{On: true, Into: into, Out: "x"}}).Folder.MoveWarning != "" {
		t.Fatal("a warning with nothing to re-download")
	}
}

// A taken name at the target is numbered, for the folder and the .part.
func TestAMoveNumbersATakenName(t *testing.T) {
	files := manyFiles("", 40, 256<<10)
	data := buildZip(t, files)
	srv := newZipServer(data)
	defer srv.Close()
	root := t.TempDir()
	from, to := filepath.Join(root, "a"), filepath.Join(root, "b")
	os.MkdirAll(filepath.Join(to, "big"), 0o755)
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 5*time.Millisecond)}, main: "eth"}
	d := startUntil(t, env, State{ID: "m5", URL: srv.URL + "/dl", Dir: from, Size: -1, Folder: &Folder{On: true, Into: from}}, someExtracted)
	d.Stop(StatusPaused)
	if err := d.SetFolderInto(to); err != nil {
		t.Fatal(err)
	}
	st := d.State()
	if st.Folder.Out != filepath.Join(to, "big (1)") {
		t.Fatalf("out %s", st.Folder.Out)
	}
	st.Status = StatusQueued
	d2 := runToEnd(t, env, st)
	if s := d2.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkTree(t, filepath.Join(to, "big (1)"), files, "")
}

// A paused download moved to another drive waits to move until it resumes,
// and says so; then it moves and ends correct.
func TestAPausedCrossDriveMoveHappensOnResume(t *testing.T) {
	otherDrive(t)
	files := manyFiles("", 40, 256<<10)
	data := buildZip(t, files)
	srv := newZipServer(data)
	defer srv.Close()
	root := t.TempDir()
	from, to := filepath.Join(root, "a"), filepath.Join(root, "b")
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 5*time.Millisecond)}, main: "eth"}
	d := startUntil(t, env, State{ID: "m6", URL: srv.URL + "/dl", Dir: from, Size: -1, Folder: &Folder{On: true, Into: from}}, someExtracted)
	d.Stop(StatusPaused)
	if err := d.SetFolderInto(to); err != nil {
		t.Fatal(err)
	}
	if s := d.Summary(); !strings.Contains(s.Note, "moves to "+to+" when it resumes") {
		t.Fatalf("note %q", s.Note)
	}
	st := d.State()
	st.Status = StatusQueued
	d.dropParked() // d2 runs in its place, as after an engine restart
	d2 := runToEnd(t, env, st)
	if s := d2.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkTree(t, filepath.Join(to, "big"), files, "")
	if left := names(t, from); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

// A crash after a move between drives copied everything but before its state
// was saved: the old copies are gone, so the copy is taken as it is.
func TestAMoveInterruptedAfterTheCopyKeepsTheCopy(t *testing.T) {
	root := t.TempDir()
	newOut := filepath.Join(root, "b", "big")
	os.MkdirAll(newOut, 0o755)
	os.WriteFile(filepath.Join(newOut, "x.txt"), []byte("x"), 0o644)
	newPart := filepath.Join(root, "b", "big.zip"+partSuffix)
	os.WriteFile(newPart, []byte("zip"), 0o644)
	d := New(&testEnv{}, State{ID: "m7", Dir: filepath.Join(root, "a"), Filename: "big.zip", Size: 3, Allocated: true, RangeOK: true, Status: StatusPaused,
		Folder: &Folder{On: true, Into: filepath.Join(root, "a"), Out: filepath.Join(root, "a", "big"), Moving: filepath.Join(root, "b"), MoveOut: newOut, MovePart: newPart}})
	otherDrive(t)
	d.relocate(t.Context())
	st := d.State()
	if st.Folder.Out != newOut || st.Dir != filepath.Join(root, "b") || st.Folder.Moving != "" || st.Folder.MoveOut != "" {
		t.Fatalf("after the retry: dir %s, %+v", st.Dir, st.Folder)
	}
	checkFile(t, filepath.Join(newOut, "x.txt"), []byte("x"))
}

// Switched on late with the .part on another drive than the folder it
// extracts into: the .part is copied there first, and it ends as a folder.
func TestSwitchingOnLateAcrossDrivesCopiesThePart(t *testing.T) {
	otherDrive(t)
	files := manyFiles("", 32, 512<<10)
	data := buildZip(t, files)
	srv := newZipServer(data)
	defer srv.Close()
	first, into := t.TempDir(), t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 4*time.Millisecond)}, main: "eth"}
	d := startUntil(t, env, State{ID: "m8", URL: srv.URL + "/dl", Dir: first, Size: -1},
		func(d *Download) bool { return d.Summary().Done >= 2<<20 })
	if err := d.SetFolderInto(into); err != nil {
		t.Fatal(err)
	}
	if err := d.SetFolder(true); err != nil {
		t.Fatal(err)
	}
	waitCompleted(t, env, d)
	checkTree(t, filepath.Join(into, "big"), files, "")
	if got := names(t, into); len(got) != 1 {
		t.Fatalf("left in the folder: %v", got)
	}
	if got := names(t, first); len(got) != 0 {
		t.Fatalf("left where it started: %v", got)
	}
}

// Between drives too, a file held open stops the move before anything is
// copied: the old folder couldn't be deleted after.
func TestACrossDriveMoveWithAFileHeldOpenChangesNothing(t *testing.T) {
	otherDrive(t)
	files := manyFiles("", 40, 256<<10)
	data := buildZip(t, files)
	srv := newZipServer(data)
	defer srv.Close()
	root := t.TempDir()
	from, to := filepath.Join(root, "a"), filepath.Join(root, "b")
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 5*time.Millisecond)}, main: "eth"}
	d := startUntil(t, env, State{ID: "m9", URL: srv.URL + "/dl", Dir: from, Size: -1, Folder: &Folder{On: true, Into: from}}, someExtracted)
	d.Stop(StatusPaused)
	out := d.State().Folder.Out
	held, err := os.Open(filepath.Join(out, filepath.FromSlash(files[0].name)))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := d.SetFolderInto(to); err != nil {
		t.Fatal(err) // between drives it waits for the next run
	}
	st := d.State()
	st.Status = StatusQueued
	d2 := New(env, st)
	if err := d2.relocate(t.Context()); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("moving with a file open: %v", err)
	}
	f := d2.State().Folder
	if f.Out != out || f.Moving != "" || !strings.Contains(f.MoveError, "in use") {
		t.Fatalf("folder after the refused move: %+v", f)
	}
	if exists(to) && len(names(t, to)) > 0 {
		t.Fatalf("something arrived at the target: %v", names(t, to))
	}
}

// Removed with its files after a crash midway through a move between drives:
// the half-made copies go too, not only the folder and the .part.
func TestRemovingAfterACrashMidMoveDeletesTheCopies(t *testing.T) {
	root := t.TempDir()
	from, to := filepath.Join(root, "a"), filepath.Join(root, "b")
	oldOut, newOut := filepath.Join(from, "big"), filepath.Join(to, "big")
	oldPart, newPart := filepath.Join(from, "big.zip"+partSuffix), filepath.Join(to, "big.zip"+partSuffix)
	for _, dir := range []string{oldOut, newOut} {
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x"), 0o644)
	}
	for _, p := range []string{oldPart, newPart} {
		os.WriteFile(p, []byte("zip"), 0o644)
	}
	d := New(&testEnv{}, State{ID: "m9", Dir: from, Filename: "big.zip", Size: 3, Allocated: true, RangeOK: true, Status: StatusPaused,
		Folder: &Folder{On: true, Into: from, Out: oldOut, Moving: to, MoveOut: newOut, MovePart: newPart}})
	d.Discard(true)
	for _, p := range []string{oldOut, newOut, oldPart, newPart} {
		if exists(p) {
			t.Errorf("%s is still there", p)
		}
	}
}

// A move asked for while stopped is called off when the mode goes off, so
// switching it on again doesn't move to a folder chosen before.
func TestSwitchingOffCallsOffAPendingMove(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "a")
	os.MkdirAll(filepath.Join(from, "big"), 0o755)
	d := New(&testEnv{}, State{ID: "m10", Dir: from, Filename: "big.zip", Size: 3, RangeOK: true, Probed: true, Status: StatusPaused,
		Folder: &Folder{On: true, Into: from, Out: filepath.Join(from, "big"), Moving: filepath.Join(root, "b"), MoveError: "earlier"}})
	if err := d.SetFolder(false); err != nil {
		t.Fatal(err)
	}
	if err := d.SetFolder(true); err != nil {
		t.Fatal(err)
	}
	if f := d.State().Folder; f.Moving != "" || f.MoveError != "" {
		t.Fatalf("still pending: moving %q, error %q", f.Moving, f.MoveError)
	}
}
