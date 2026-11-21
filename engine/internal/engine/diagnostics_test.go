package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The report "Copy diagnostics" makes says what the download is doing and
// carries no secret: no password, query string, cookie or token, whether from
// the download's own URLs or from engine.log.
func TestDiagnosticsCarryNoSecrets(t *testing.T) {
	dir := t.TempDir()
	e, err := open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	e.settings.MaxParallel = 0
	id, err := e.Add(AddRequest{URL: "https://bob:hunter2@files.example/f.iso?token=abc123&sig=zzsig", PageURL: "https://files.example/page?session=s3ss"})
	if err != nil {
		t.Fatal(err)
	}
	log := "2026/11/21 18:00:00 download " + id + " via eth: GET https://files.example/f.iso?token=abc123 refused\n" +
		"2026/11/21 18:00:01 download " + id + " resolve: cookie=c00kie; Authorization: Bearer-t0ken password=pw9\n" +
		"2026/11/21 18:00:02 download other: not about it\n"
	if err := os.WriteFile(filepath.Join(dir, "engine.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := e.Diagnostics(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"hunter2", "abc123", "zzsig", "s3ss", "c00kie", "Bearer-t0ken", "pw9", "not about it"} {
		if strings.Contains(r, secret) {
			t.Errorf("the report has %q:\n%s", secret, r)
		}
	}
	for _, want := range []string{"URL: https://bob@files.example/f.iso?…", "Landing page: https://files.example/page?…", "via eth: GET https://files.example/f.iso?… refused", "Connections:", "engine.log (last 2 lines about it):"} {
		if !strings.Contains(r, want) {
			t.Errorf("the report lacks %q:\n%s", want, r)
		}
	}
}
