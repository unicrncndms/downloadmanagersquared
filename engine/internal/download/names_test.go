package download

import "testing"


// The same samples as "fmtBytes" in extension/test/list.test.js: the engine's
// text and the pages show a size the same way.
func TestHumanMatchesTheListsRule(t *testing.T) {
	for _, c := range []struct {
		n    int64
		want string
	}{
		{0, "0 B"}, {999, "999 B"}, {1500, "1.5 KB"}, {150e6, "150 MB"}, {3435973837, "3.4 GB"}, {-1, "?"},
	} {
		if got := Human(c.n); got != c.want {
			t.Errorf("Human(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
