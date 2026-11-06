package download

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeFilename(t *testing.T) {
	for in, want := range map[string]string{
		"":             "",
		" . ":          "",
		"a:b?.iso":     "a_b_.iso",
		`dir\..\x.zip`: "dir_.._x.zip",
		"COM5.iso":     "_COM5.iso",
		"CON.tar.gz":   "_CON.tar.gz",
		"lpt9":         "_lpt9",
		"report.":      "report",
		"COMPUTER.iso": "COMPUTER.iso",
	} {
		if got := SanitizeFilename(in); got != want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
	long := SanitizeFilename(strings.Repeat("é", 150) + ".mkv")
	if len(long) > 200 || !utf8.ValidString(long) || !strings.HasSuffix(long, ".mkv") {
		t.Errorf("long name cut to %q (%d bytes)", long, len(long))
	}
}
