package relver

import (
	"encoding/json"
	"os"
	"testing"
)

// testdata/clash.json is shared with extension/test/version.test.js, so the
// engine's rule and the popup's can't drift apart.
func TestClash(t *testing.T) {
	b, err := os.ReadFile("testdata/clash.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases [][3]any
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		engine, ext, want := c[0].(string), c[1].(string), c[2].(bool)
		if got := Clash(engine, ext); got != want {
			t.Errorf("Clash(%q, %q) = %v, want %v", engine, ext, got, want)
		}
	}
}
