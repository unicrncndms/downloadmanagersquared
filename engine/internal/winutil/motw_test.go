package winutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarkFromInternet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.exe")
	if err := os.WriteFile(path, []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := MarkFromInternet(path, "https://user:pw@example.com/x.exe", "https://example.com/page"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path + ":Zone.Identifier")
	if err != nil {
		t.Fatal(err)
	}
	want := "[ZoneTransfer]\r\nZoneId=3\r\nReferrerUrl=https://example.com/page\r\nHostUrl=https://example.com/x.exe\r\n"
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm()&0o200 != 0 {
		t.Fatal("the file is no longer read-only")
	}
	// No URL worth recording: the zone alone.
	if err := MarkFromInternet(path, "magnet:?xt=urn:btih:abc", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path + ":Zone.Identifier"); strings.Contains(string(got), "Url") {
		t.Fatalf("got %q, want the zone only", got)
	}
}
