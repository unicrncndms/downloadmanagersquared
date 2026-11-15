package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func makeZip(t *testing.T, names ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range names {
		w, _ := zw.Create(n)
		w.Write([]byte("exe:" + n))
	}
	zw.Close()
	return buf.Bytes()
}

// Download takes only an exact dms-engine.exe, from a zip whose checksum
// matches, from an allowed address.
func TestDownload(t *testing.T) {
	good := makeZip(t, "LICENSE", "../evil.exe", "dms-engine.exe")
	none := makeZip(t, "dms-host.exe", "sub/dms-engine.exe")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/none" {
			w.Write(none)
			return
		}
		w.Write(good)
	}))
	defer srv.Close()
	old := assetPrefix
	assetPrefix = srv.URL
	defer func() { assetPrefix = old }()
	sum := func(b []byte) string { h := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(h[:]) }
	ctx := context.Background()

	exe, err := Download(ctx, srv.Client(), srv.URL+"/z", sum(good))
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(exe))
	if b, _ := os.ReadFile(exe); string(b) != "exe:dms-engine.exe" || filepath.Base(exe) != "dms-engine.exe" {
		t.Fatalf("exe %s holds %q", exe, b)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(exe)), "evil.exe")); err == nil {
		t.Fatal("an archive-named path was extracted")
	}
	for name, c := range map[string]struct{ url, digest string }{
		"wrong digest": {srv.URL + "/z", sum([]byte("other"))},
		"no digest":    {srv.URL + "/z", ""},
		"no exe entry": {srv.URL + "/none", sum(none)},
		"not GitHub":   {"https://example.com/z", sum(good)},
	} {
		if exe, err := Download(ctx, srv.Client(), c.url, c.digest); err == nil {
			os.RemoveAll(filepath.Dir(exe))
			t.Errorf("%s: no error", name)
		}
	}
}
