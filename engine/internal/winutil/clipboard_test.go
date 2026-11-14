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
