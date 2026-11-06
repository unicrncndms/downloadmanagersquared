package download

import (
	"strings"
	"testing"
)

// What Details is told about the toggle: shown for a .zip (or once asked
// for), and why it can't go on.
func TestTheSummaryOffersTheToggleAndSaysWhyNot(t *testing.T) {
	cases := []struct {
		name  string
		st    State
		offer bool   // a Folder summary at all
		why   string // part of the reason it can't go on, "" when it can
	}{
		{"a zip", State{Filename: "big.zip"}, true, ""},
		{"name not known yet", State{}, false, ""},
		{"not a zip", State{Filename: "big.iso"}, false, ""},
		{"asked for, then not a zip", State{Filename: "big.iso", Folder: &Folder{}}, true, ".zip"},
		{"a typed hash", State{Filename: "big.zip", SHA256: strings.Repeat("a", 64)}, true, "checksum"},
		{"no ranges", State{Filename: "big.zip", Probed: true, Size: 10}, true, "split downloading"},
		{"ranges", State{Filename: "big.zip", Probed: true, RangeOK: true, Size: 10}, true, ""},
	}
	for _, c := range cases {
		c.st.ID, c.st.Status = "s", StatusPaused
		f := New(&testEnv{}, c.st).Summary().Folder
		if (f != nil) != c.offer {
			t.Errorf("%s: folder summary %+v, want offered %v", c.name, f, c.offer)
			continue
		}
		if f == nil {
			continue
		}
		if c.why == "" && f.Why != "" || !strings.Contains(f.Why, c.why) {
			t.Errorf("%s: why %q, want it to mention %q", c.name, f.Why, c.why)
		}
	}
}

// Once something is freed it is locked, and the summary says so.
func TestALockedFolderSaysSo(t *testing.T) {
	d := New(&testEnv{}, State{ID: "l", Filename: "big.zip", Status: StatusPaused,
		Folder: &Folder{On: true, Into: `C:\x`, Freed: []Range{{0, 10}}}})
	if f := d.Summary().Folder; f == nil || !f.On || !f.Locked || !strings.Contains(f.Why, "freed") {
		t.Fatalf("summary %+v, want on and locked, saying why", f)
	}
}
