package winutil

import (
	"testing"
	"unicode/utf8"
)

func TestCutUTF16StopsAtACharacterBoundary(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
	}{
		{"DMS²  Idle", 127, "DMS²  Idle"},
		{"DMS²", 3, "DMS"}, // ² is one UTF-16 unit but two bytes: cut before it, not inside
		{"ab😀cd", 3, "ab"}, // the emoji takes two units: it doesn't fit in the third
		{"ab😀cd", 4, "ab😀"},
		{"Wi‑Fi: 1.0 MB/s", 3, "Wi‑"}, // a non-breaking hyphen, three bytes in UTF-8
		{"", 5, ""},
	} {
		got := CutUTF16(c.in, c.max)
		if got != c.want || !utf8.ValidString(got) {
			t.Errorf("CutUTF16(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}
