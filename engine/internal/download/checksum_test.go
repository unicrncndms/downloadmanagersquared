package download

import (
	"strings"
	"testing"
)

func TestParseChecksum(t *testing.T) {
	sha, md, err := ParseChecksum(" SHA256: " + strings.Repeat("AB", 32))
	if err != nil || sha != strings.Repeat("ab", 32) || md != "" {
		t.Fatalf("sha %q md %q err %v", sha, md, err)
	}
	if _, md, err = ParseChecksum(strings.Repeat("0f", 16)); err != nil || md == "" {
		t.Fatalf("md %q err %v", md, err)
	}
	if _, _, err = ParseChecksum("abc"); err == nil {
		t.Fatal("a short checksum was taken")
	}
	if _, _, err = ParseChecksum(strings.Repeat("zz", 16)); err == nil {
		t.Fatal("non-hex was taken")
	}
}
