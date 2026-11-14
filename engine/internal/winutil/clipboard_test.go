package winutil

import (
	"slices"
	"testing"
)

func TestLinksFindsWebAndMagnetLinks(t *testing.T) {
	text := "see https://a.example/f.zip and\r\nMAGNET:?xt=urn:btih:abc, http:// ftp://x/y notes http://b.example/g"
	want := []string{"https://a.example/f.zip", "MAGNET:?xt=urn:btih:abc,", "http://b.example/g"}
	if got := Links(text); !slices.Equal(got, want) {
		t.Errorf("Links = %q, want %q", got, want)
	}
	if got := Links("nothing here"); len(got) != 0 {
		t.Errorf("Links of plain text = %q, want none", got)
	}
}

func TestUtf16TextStopsAtNulOrAtTheEnd(t *testing.T) {
	for _, c := range []struct {
		in   []uint16
		want string
	}{{[]uint16{'a', 'b', 0, 'c'}, "ab"}, {[]uint16{'a', 'b'}, "ab"}, {nil, ""}} {
		if got := utf16Text(c.in); got != c.want {
			t.Errorf("utf16Text(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
