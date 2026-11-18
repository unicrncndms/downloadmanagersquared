package resolver

import "testing"

// The cases of the resolver's onHeaders in extension/background/resolver.js,
// which isTheDownload must answer the same.
func TestIsTheDownloadAgreesWithTheExtension(t *testing.T) {
	for _, c := range []struct {
		name   string
		h      map[string]string
		status int
		want   bool
	}{
		{"attachment, any size", map[string]string{"content-type": "text/plain", "content-disposition": "attachment; filename=a.txt", "content-length": "10"}, 200, true},
		{"big binary", map[string]string{"content-type": "application/octet-stream", "content-length": "5000000"}, 200, true},
		{"binary of unknown size", map[string]string{"content-type": "application/zip"}, 200, true},
		{"small binary the page uses", map[string]string{"content-type": "application/octet-stream", "content-length": "2000"}, 200, false},
		{"empty length counts as 0", map[string]string{"content-type": "application/octet-stream", "content-length": ""}, 200, false},
		{"a page", map[string]string{"content-type": "text/html; charset=utf-8"}, 200, false},
		{"a page even as attachment", map[string]string{"content-type": "text/html", "content-disposition": "attachment"}, 200, false},
		{"an image", map[string]string{"content-type": "image/png", "content-length": "9000000"}, 200, false},
		{"json", map[string]string{"content-type": "application/json", "content-length": "9000000"}, 200, false},
		{"a redirect", map[string]string{"content-type": "application/zip", "content-length": "9000000"}, 302, false},
		{"206 sized by its range", map[string]string{"content-type": "application/zip", "content-length": "100", "content-range": "bytes 0-99/7000000"}, 206, true},
		{"no type at all, big", map[string]string{"content-length": "2000000"}, 200, true},
	} {
		if got := isTheDownload(c.h, c.status); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
