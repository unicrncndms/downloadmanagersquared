package download

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSetHashesTakesOnlyHexOfTheRightLength(t *testing.T) {
	d := New(&testEnv{}, State{ID: "h", Status: StatusQueued})
	good := strings.Repeat("ab", 32)
	for _, c := range []struct{ sha, md string }{
		{"abc", ""},
		{"", "abc"},
		{"SHA256: " + good, ""},
		{strings.Repeat("zz", 32), ""},
		{good, good}, // a SHA-256 in the MD5 field
	} {
		if err := d.SetHashes(c.sha, c.md); err == nil {
			t.Errorf("took sha %q md5 %q", c.sha, c.md)
		}
	}
	if err := d.SetHashes(" "+strings.ToUpper(good)+"\n", ""); err != nil {
		t.Fatalf("a SHA-256 in capitals with spaces around it: %v", err)
	}
	if st := d.State(); st.SHA256 != good {
		t.Fatalf("stored %q, want %q", st.SHA256, good)
	}
}

func TestChecksumOfAFinishedFileIsCheckedWhenSaved(t *testing.T) {
	data := randomData(4 << 20)
	srv := fileServer(t, data, nil)
	defer srv.Close()
	env := &testEnv{ifaces: []Iface{iface("any", 2, 0)}, main: "any"}
	d := runToEnd(t, env, State{ID: "h1", URL: srv.URL + "/file/any", Dir: t.TempDir(), Size: -1})
	if s := d.Summary(); s.Status != StatusCompleted || s.Note != "" {
		t.Fatalf("finished without a checksum: %s, note %q", s.Status, s.Note)
	}

	sha := sha256.Sum256(data)
	md := md5.Sum(data)
	if err := d.SetHashes(hex.EncodeToString(sha[:]), hex.EncodeToString(md[:])); err != nil {
		t.Fatal(err)
	}
	waitSummary(t, d, func(s Summary) bool { return s.Note == "SHA-256 and MD5 match" }, "both to match")
	if s := d.Summary(); s.Label != "Completed" || s.Tone != toneOK || s.Error != "" {
		t.Fatalf("after a match: %q %q %q", s.Label, s.Tone, s.Error)
	}

	if err := d.SetHashes(strings.Repeat("0", 64), ""); err != nil {
		t.Fatal(err)
	}
	waitSummary(t, d, func(s Summary) bool { return s.Label == "Checksum mismatch" }, "a mismatch")
	if s := d.Summary(); s.Tone != toneBad || !strings.Contains(s.Error, "SHA-256 does not match") || s.Status != StatusCompleted {
		t.Fatalf("after a mismatch: %s %q %q %q", s.Status, s.Label, s.Tone, s.Error)
	}

	if err := d.SetHashes("", ""); err != nil {
		t.Fatal(err)
	}
	if s := d.Summary(); s.Label != "Completed" || s.Error != "" || s.Note != "" {
		t.Fatalf("with the checksums cleared: %q %q note %q", s.Label, s.Error, s.Note)
	}
}

func TestChecksumMatchedWhileDownloadingIsShown(t *testing.T) {
	data := randomData(4 << 20)
	srv := fileServer(t, data, nil)
	defer srv.Close()
	sha := sha256.Sum256(data)
	env := &testEnv{ifaces: []Iface{iface("any", 2, 0)}, main: "any"}
	d := runToEnd(t, env, State{ID: "h2", URL: srv.URL + "/file/any", Dir: t.TempDir(), Size: -1,
		Status: StatusQueued, SHA256: hex.EncodeToString(sha[:])})
	if s := d.Summary(); s.Status != StatusCompleted || s.Note != "SHA-256 matches" {
		t.Fatalf("finished: %s, note %q", s.Status, s.Note)
	}
}

func TestChecksumMismatchWhileDownloadingSaysWhatToDo(t *testing.T) {
	data := randomData(4 << 20)
	srv := fileServer(t, data, nil)
	defer srv.Close()
	env := &testEnv{ifaces: []Iface{iface("any", 2, 0)}, main: "any"}
	d := runToEnd(t, env, State{ID: "h3", URL: srv.URL + "/file/any", Dir: t.TempDir(), Size: -1,
		Status: StatusQueued, MD5: strings.Repeat("0", 32)})
	if s := d.Summary(); s.Status != StatusFailed || !strings.Contains(s.Error, "MD5 does not match") || !strings.Contains(s.Error, "add it again") {
		t.Fatalf("finished: %s, %q", s.Status, s.Error)
	}
}

func waitSummary(t *testing.T, d *Download, ok func(Summary) bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok(d.Summary()) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	s := d.Summary()
	t.Fatalf("waiting for %s: %s %q note %q error %q", what, s.Status, s.Label, s.Note, s.Error)
}

// A checksum changed while the file is being verified is not covered by the
// check that ran: "match" is for the value that was checked, the new one is checked too.
func TestChecksumChangedDuringVerificationIsChecked(t *testing.T) {
	data := randomData(1 << 20)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.bin"+partSuffix), data, 0o644); err != nil {
		t.Fatal(err)
	}
	sha := sha256.Sum256(data)
	d := New(&testEnv{}, State{ID: "hv", Dir: dir, Filename: "f.bin", Status: StatusRunning, SHA256: hex.EncodeToString(sha[:])})
	real, calls := verifyFile, 0
	verifyFile = func(ctx context.Context, path, wantSHA, wantMD5 string) error {
		err := real(ctx, path, wantSHA, wantMD5)
		if calls++; calls == 1 {
			d.SetHashes(strings.Repeat("0", 64), "") // the user corrects the value meanwhile
		}
		return err
	}
	defer func() { verifyFile = real }()
	err := d.complete(context.Background())
	if !errors.Is(err, errMismatch) || calls != 2 {
		t.Fatalf("complete: %v after %d checks, want a mismatch of the new value after 2", err, calls)
	}
	if r := d.State().HashResult; r == "match" {
		t.Fatalf("HashResult %q for a value that was never checked", r)
	}
}

func TestBothChecksumsAreRightWhenBothAreSet(t *testing.T) {
	data := randomData(3*verifyBuf + 123) // several chunks, the last one short
	path := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	sha, md := sha256.Sum256(data), md5.Sum(data)
	wantSHA, wantMD5 := hex.EncodeToString(sha[:]), hex.EncodeToString(md[:])
	ctx := context.Background()
	if err := verify(ctx, path, wantSHA, wantMD5); err != nil {
		t.Fatalf("both right: %v", err)
	}
	if err := verify(ctx, path, wantSHA, strings.Repeat("0", 32)); !errors.Is(err, errMismatch) || !strings.HasPrefix(err.Error(), "MD5") {
		t.Fatalf("wrong MD5: %v", err)
	}
	if err := verify(ctx, path, strings.Repeat("0", 64), wantMD5); !errors.Is(err, errMismatch) || !strings.HasPrefix(err.Error(), "SHA-256") {
		t.Fatalf("wrong SHA-256: %v", err)
	}
}

// cancelAfterRead cancels the pass after its first read.
type cancelAfterRead struct {
	r      io.Reader
	cancel context.CancelFunc
	n      int
}

func (c *cancelAfterRead) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	c.cancel()
	return n, err
}

func TestCancelStopsTheChecksumPass(t *testing.T) {
	data := randomData(64 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &cancelAfterRead{r: bytes.NewReader(data), cancel: cancel}
	err := hashFile(ctxReader{ctx, src}, sha256.New(), md5.New())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("hashFile after cancel: %v, want context.Canceled", err)
	}
	if src.n >= len(data)/2 {
		t.Fatalf("read %d of %d bytes after cancel", src.n, len(data))
	}
}
