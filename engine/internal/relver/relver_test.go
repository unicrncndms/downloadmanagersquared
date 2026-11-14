package relver

import "testing"

func TestClash(t *testing.T) {
	for _, c := range []struct {
		engine, ext string
		want        bool
	}{
		{"1.2.3", "1.9.0", false},
		{"v1.2.3", "1.2.4", false},
		{"1.0.0", "2.0.0", true},
		{"0.4.1", "0.4.9", false},
		{"0.4.1", "0.5.0", true},
		{"dev", "2.0.0", false},
		{"0.0.0", "2.0.0", false},
		{"", "2.0.0", false},
		{"2.0.0", "2.0.0-beta", false},
	} {
		if got := Clash(c.engine, c.ext); got != c.want {
			t.Errorf("Clash(%q, %q) = %v, want %v", c.engine, c.ext, got, c.want)
		}
	}
}
