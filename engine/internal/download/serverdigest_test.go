package download

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gcsServer serves data the way Google Cloud Storage does: every answer, a
// range too, carries the whole object's MD5 in x-goog-hash. md5For gives the
// MD5 each interface's answers claim.
func gcsServer(t *testing.T, data []byte, md5For func(iface string) []byte) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sum := md5For(r.Header.Get("X-Iface"))
		w.Header().Add("x-goog-hash", "crc32c=8bA1Tw==")
		w.Header().Add("x-goog-hash", "md5="+base64.StdEncoding.EncodeToString(sum))
		w.Header().Set("x-goog-stored-content-encoding", "identity")
		w.Header().Set("Content-Disposition", `attachment; filename="big.bin"`)
		http.ServeContent(w, r, "big.bin", t0, bytes.NewReader(data))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestVersionReadsTheWholeObjectMD5OfGCS(t *testing.T) {
	sum := md5.Sum([]byte("x"))
	b64 := base64.StdEncoding.EncodeToString(sum[:])
	want := hex.EncodeToString(sum[:])
	for _, c := range []struct {
		name string
		h    http.Header
		want string
	}{
		{"one header per hash", http.Header{"X-Goog-Hash": {"crc32c=8bA1Tw==", "md5=" + b64}}, want},
		{"both in one header", http.Header{"X-Goog-Hash": {"crc32c=8bA1Tw==, md5=" + b64}}, want},
		{"stored as identity", http.Header{"X-Goog-Hash": {"md5=" + b64}, "X-Goog-Stored-Content-Encoding": {"identity"}}, want},
		{"composite object: crc32c only", http.Header{"X-Goog-Hash": {"crc32c=8bA1Tw=="}}, ""},
		{"stored gzipped, sent unzipped", http.Header{"X-Goog-Hash": {"md5=" + b64}, "X-Goog-Stored-Content-Encoding": {"gzip"}}, ""},
		{"sent encoded", http.Header{"X-Goog-Hash": {"md5=" + b64}, "Content-Encoding": {"gzip"}}, ""},
		{"not an MD5", http.Header{"X-Goog-Hash": {"md5=AAAA"}}, ""},
		{"Content-MD5 is the body's, a range's on a 206", http.Header{"Content-Md5": {b64}}, ""},
	} {
		if got := versionOf(c.h).MD5; got != c.want {
			t.Errorf("%s: MD5 %q, want %q", c.name, got, c.want)
		}
	}
}

func TestServersMD5DecidesTheVersion(t *testing.T) {
	a, b := strings.Repeat("a", 32), strings.Repeat("b", 32)
	date := "Fri, 02 Jan 2026 03:04:05 GMT"
	for _, c := range []struct {
		name string
		x, y Version
		same bool
	}{
		{"same MD5", Version{MD5: a}, Version{MD5: a}, true},
		{"other MD5, same date and etag", Version{MD5: a, ETag: `"e"`, LastModified: date}, Version{MD5: b, ETag: `"e"`, LastModified: date}, false},
		{"one side has none", Version{MD5: a, LastModified: date}, Version{LastModified: date}, true},
	} {
		if got := c.x.same(c.y); got != c.same {
			t.Errorf("%s: same = %v, want %v", c.name, got, c.same)
		}
	}
}

func TestFileIsCheckedAgainstTheServersMD5(t *testing.T) {
	data := randomData(4 << 20)
	sum := md5.Sum(data)
	srv := gcsServer(t, data, func(string) []byte { return sum[:] })
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 0), iface("wifi", 2, 0)}, main: "eth"}
	d := runToEnd(t, env, State{ID: "g1", URL: srv.URL + "/big.bin", Dir: dir, Size: -1})
	s := d.Summary()
	if s.Status != StatusCompleted || s.Note != "MD5 from the server matches" {
		t.Fatalf("finished: %s %q, note %q", s.Status, s.Error, s.Note)
	}
	if st := d.State(); st.Version.MD5 != hex.EncodeToString(sum[:]) || st.MD5 != "" {
		t.Fatalf("saved server MD5 %q, pasted %q", st.Version.MD5, st.MD5)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
}

func TestServersMD5MismatchKeepsThePartFile(t *testing.T) {
	data := randomData(4 << 20)
	wrong := md5.Sum([]byte("another file"))
	srv := gcsServer(t, data, func(string) []byte { return wrong[:] })
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("any", 2, 0)}, main: "any"}
	d := runToEnd(t, env, State{ID: "g2", URL: srv.URL + "/big.bin", Dir: dir, Size: -1})
	s := d.Summary()
	if s.Status != StatusFailed || !strings.Contains(s.Error, "MD5 from the server does not match") {
		t.Fatalf("finished: %s %q", s.Status, s.Error)
	}
	if _, err := os.Stat(filepath.Join(dir, "big.bin"+partSuffix)); err != nil {
		t.Fatalf("the part file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "big.bin")); err == nil {
		t.Fatal("a file that doesn't match was renamed into place")
	}
}

func TestPastedChecksumWinsOverTheServers(t *testing.T) {
	data := randomData(4 << 20)
	wrong := md5.Sum([]byte("another file"))
	sha := sha256.Sum256(data)
	srv := gcsServer(t, data, func(string) []byte { return wrong[:] })
	env := &testEnv{ifaces: []Iface{iface("any", 2, 0)}, main: "any"}
	d := runToEnd(t, env, State{ID: "g3", URL: srv.URL + "/big.bin", Dir: t.TempDir(), Size: -1,
		Status: StatusQueued, SHA256: hex.EncodeToString(sha[:])})
	if s := d.Summary(); s.Status != StatusCompleted || s.Note != "SHA-256 matches" {
		t.Fatalf("finished: %s %q, note %q", s.Status, s.Error, s.Note)
	}
}

// Two connections whose servers vouch for different files: the second sits
// out, as for another version.
func TestConnectionWhoseServerSendsAnotherMD5SitsOut(t *testing.T) {
	data := randomData(16 << 20)
	sum := md5.Sum(data)
	other := md5.Sum([]byte("v2"))
	srv := gcsServer(t, data, func(ifc string) []byte {
		if ifc == "wifi" {
			return other[:]
		}
		return sum[:]
	})
	dir := t.TempDir()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 0), iface("wifi", 2, 0)}, main: "eth"}
	d := runToEnd(t, env, State{ID: "g4", URL: srv.URL + "/big.bin", Dir: dir, Size: -1})
	s := d.Summary()
	if s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	if l := s.Links["wifi"]; l.Status != LinkMismatch {
		t.Errorf("wifi's server sends another MD5 and should sit out, got %+v", l)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
}
