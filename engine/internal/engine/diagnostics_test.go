package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"downloadmanagersquared/engine/internal/logprivacy"
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
	// Log privacy Off: as before the setting, without secrets; Standard: no
	// links' paths either; Increased: no sites.
	for level, wants := range map[logprivacy.Level][]string{
		logprivacy.Off:      {"URL: https://bob@files.example/f.iso?…", "Landing page: https://files.example/page?…", "via eth: GET https://files.example/f.iso?… refused"},
		logprivacy.Standard: {"URL: https://files.example/…", "Landing page: https://files.example/…", "via eth: GET https://files.example/… refused"},
		logprivacy.Strict:   {"URL: https://…", "via eth: GET https://… refused"},
	} {
		logprivacy.Set(level)
		r, err := e.Diagnostics(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"hunter2", "abc123", "zzsig", "s3ss", "c00kie", "Bearer-t0ken", "pw9", "not about it"} {
			if strings.Contains(r, secret) {
				t.Errorf("level %d: the report has %q:\n%s", level, secret, r)
			}
		}
		for _, want := range append(wants, "Connections:", "engine.log (last 2 lines about it):") {
			if !strings.Contains(r, want) {
				t.Errorf("level %d: the report lacks %q:\n%s", level, want, r)
			}
		}
		if level == logprivacy.Strict && strings.Contains(r, "files.example") {
			t.Errorf("Increased: the report names the site:\n%s", r)
		}
	}
	logprivacy.Set(logprivacy.Standard)
}
