package download

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func TestDuplicatesReadsTheLinkHeader(t *testing.T) {
	base, _ := url.Parse("https://a.example/pub/f.iso")
	got := duplicates([]string{
		`<https://c.example/f.iso>; rel=duplicate; pri=2, <ftp://x.example/f.iso>; rel=duplicate`,
		`<https://b.example/f.iso>; rel="duplicate"; pri=1; geo=de`,
		`</other/f.iso>; rel=duplicate`,
		`<https://d.example/f.iso.meta4>; rel=describedby; type="application/metalink4+xml"`,
	}, base)
	want := []string{"https://b.example/f.iso", "https://c.example/f.iso", "https://a.example/other/f.iso"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestVersionReadsDigestAndReprDigest(t *testing.T) {
	sum := sha256.Sum256([]byte("x"))
	b64, want := base64.StdEncoding.EncodeToString(sum[:]), hex.EncodeToString(sum[:])
	for _, c := range []struct {
		name string
		h    http.Header
		want string
	}{
		{"Digest", http.Header{"Digest": {"MD5=AAAA, SHA-256=" + b64}}, want},
		{"Repr-Digest", http.Header{"Repr-Digest": {"sha-256=:" + b64 + ":"}}, want},
		{"Content-Digest is the body's", http.Header{"Content-Digest": {"sha-256=:" + b64 + ":"}}, ""},
		{"sent encoded", http.Header{"Digest": {"SHA-256=" + b64}, "Content-Encoding": {"gzip"}}, ""},
	} {
		if got := versionOf(c.h).SHA256; got != c.want {
			t.Errorf("%s: SHA-256 %q, want %q", c.name, got, c.want)
		}
	}
}

// mirror serves data the way one mirror would: its own dates, extra headers,
// and a count of the bytes it sent for chunks.
func mirror(t *testing.T, data []byte, mod time.Time, header func(h http.Header)) (*httptest.Server, *atomic.Int64) {
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if header != nil {
			header(w.Header())
		}
		if r.Header.Get("Range") != "bytes=0-0" {
			w = countingWriter{w, &n}
		}
		http.ServeContent(w, r, "big.iso", mod, bytes.NewReader(data))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func digest(data []byte) string {
	s := sha256.Sum256(data)
	return "SHA-256=" + base64.StdEncoding.EncodeToString(s[:])
}

func sha256Hex(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

func TestEachInterfaceFetchesFromItsOwnMirror(t *testing.T) {
	data := randomData(16 << 20)
	a, na := mirror(t, data, t0, nil)
	b, nb := mirror(t, data, t0.Add(time.Hour), nil) // in step, but its own date
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 20*time.Millisecond), iface("wifi", 2, 20*time.Millisecond)}, main: "eth"}
	d := runToEnd(t, env, State{
		ID: "m1", URL: a.URL + "/big.iso", Dir: t.TempDir(), Filename: "big.iso", Size: -1,
		Mirrors:  []Mirror{{URL: a.URL + "/big.iso"}, {URL: b.URL + "/big.iso"}},
		MetaSize: int64(len(data)), SHA256: sha256Hex(data),
	})
	s := d.Summary()
	if s.Status != StatusCompleted || s.Note != "SHA-256 matches" {
		t.Fatalf("status %s %q, note %q", s.Status, s.Error, s.Note)
	}
	if na.Load() == 0 || nb.Load() == 0 {
		t.Fatalf("mirror A sent %d bytes, B %d: both should have", na.Load(), nb.Load())
	}
	if s.Links["eth"].Mirror != hostOf(a.URL) || s.Links["wifi"].Mirror != hostOf(b.URL) {
		t.Fatalf("eth on %q, wifi on %q", s.Links["eth"].Mirror, s.Links["wifi"].Mirror)
	}
	got, _ := os.ReadFile(filepath.Join(s.Dir, s.Filename))
	if !bytes.Equal(got, data) {
		t.Fatal("finished file differs")
	}
}

func TestAMirrorServingAnotherFileIsDropped(t *testing.T) {
	data := randomData(16 << 20)
	other := randomData(16 << 20)
	other[0] ^= 1
	good, _ := mirror(t, data, t0, nil)
	short, _ := mirror(t, data[:len(data)-1], t0, nil)
	wrong, _ := mirror(t, other, t0, func(h http.Header) { h.Set("Digest", digest(other)) })
	last, nl := mirror(t, data, t0, func(h http.Header) { h.Set("Digest", digest(data)) })
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 40*time.Millisecond), iface("wifi", 2, 5*time.Millisecond)}, main: "eth"} // eth slow: wifi joins late and still finds work
	d := runToEnd(t, env, State{
		ID: "m2", URL: good.URL + "/big.iso", Dir: t.TempDir(), Filename: "big.iso", Size: -1,
		Mirrors:  []Mirror{{URL: good.URL + "/big.iso"}, {URL: short.URL + "/big.iso"}, {URL: wrong.URL + "/big.iso"}, {URL: last.URL + "/big.iso"}},
		MetaSize: int64(len(data)), SHA256: sha256Hex(data),
	})
	s := d.Summary()
	if s.Status != StatusCompleted || s.Note != "SHA-256 matches" {
		t.Fatalf("status %s %q, note %q", s.Status, s.Error, s.Note)
	}
	for i, want := range []bool{false, true, true, false} {
		if got := s.Mirrors[i].Dropped != ""; got != want {
			t.Errorf("mirror %d dropped %v (%q), want %v", i, got, s.Mirrors[i].Dropped, want)
		}
	}
	if nl.Load() == 0 {
		t.Fatal("wifi never got to the last good mirror")
	}
}

func TestLinkDuplicateWithADigestAddsMirrors(t *testing.T) {
	data := randomData(16 << 20)
	b, nb := mirror(t, data, t0.Add(time.Hour), nil)
	a, _ := mirror(t, data, t0, func(h http.Header) {
		h.Set("Digest", digest(data))
		h.Set("Link", "<"+b.URL+"/big.iso>; rel=duplicate")
	})
	env := &testEnv{ifaces: []Iface{iface("eth", 1, 40*time.Millisecond), iface("wifi", 2, 5*time.Millisecond)}, main: "eth"} // eth slow: wifi joins late and still finds work
	d := runToEnd(t, env, State{ID: "m3", URL: a.URL + "/big.iso", Cookie: "s=1", Dir: t.TempDir(), Size: -1})
	s := d.Summary()
	if s.Status != StatusCompleted || s.Note != "SHA-256 from the server matches" {
		t.Fatalf("status %s %q, note %q", s.Status, s.Error, s.Note)
	}
	if len(s.Mirrors) != 2 || nb.Load() == 0 {
		t.Fatalf("mirrors %+v, B sent %d bytes", s.Mirrors, nb.Load())
	}
	if l := d.State().Links["wifi"]; l.URL != b.URL+"/big.iso" || l.Cookie != "" {
		t.Fatalf("wifi's link %q with cookie %q: the mirror, without the site's cookie", l.URL, l.Cookie)
	}
}

func TestLinkDuplicateWithoutADigestIsIgnored(t *testing.T) {
	data := randomData(8 << 20)
	b, nb := mirror(t, data, t0, nil)
	a, _ := mirror(t, data, t0, func(h http.Header) { h.Set("Link", "<"+b.URL+"/big.iso>; rel=duplicate") })
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 0), iface("wifi", 2, 0)}, main: "eth"}
	d := runToEnd(t, env, State{ID: "m4", URL: a.URL + "/big.iso", Dir: t.TempDir(), Size: -1})
	if s := d.Summary(); s.Status != StatusCompleted || len(s.Mirrors) != 0 || nb.Load() != 0 {
		t.Fatalf("status %s, mirrors %+v, B sent %d bytes", s.Status, s.Mirrors, nb.Load())
	}
}
