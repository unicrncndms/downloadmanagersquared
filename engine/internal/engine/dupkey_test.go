package engine

import "testing"

func TestLinkKeyIgnoresTokens(t *testing.T) {
	same := [][2]string{
		{"https://cdn.example/f.iso?token=aaa&expires=1", "https://CDN.example/f.iso?expires=2&token=bbb"},
		{"https://s3.example/b/f.iso?X-Amz-Signature=1&X-Amz-Date=2", "https://s3.example/b/f.iso?X-Amz-Date=3"},
		{"https://x.example/f.zip?dl=Q7mZk2LpR9xT4vWb8NcY1", "https://x.example/f.zip?dl=a1B2c3D4e5F6g7H8i9J0k"}, // mixed-case run
		{"https://x.example/f.zip?file=a.zip#top", "https://x.example/f.zip?file=a.zip"},
		{"https://u:p@x.example/f.zip", "https://x.example/f.zip"},
	}
	for _, c := range same {
		if linkKey(c[0]) != linkKey(c[1]) {
			t.Errorf("%q and %q should be one link: %q vs %q", c[0], c[1], linkKey(c[0]), linkKey(c[1]))
		}
	}
	different := [][2]string{
		{"https://x.example/f.zip?file=a.zip", "https://x.example/f.zip?file=b.zip"},
		{"https://x.example/a.zip?token=1", "https://x.example/b.zip?token=1"},
		{"https://x.example/get?name=ubuntu-24-desktop-amd64", "https://x.example/get?name=ubuntu-24-server-amd64"},
		{"https://x.example/f.zip", "http://x.example/f.zip"},
	}
	for _, c := range different {
		if linkKey(c[0]) == linkKey(c[1]) {
			t.Errorf("%q and %q should differ", c[0], c[1])
		}
	}
}
