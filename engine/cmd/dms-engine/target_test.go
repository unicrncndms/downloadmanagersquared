package main

import (
	"strings"
	"testing"

	"downloadmanagersquared/engine/internal/winutil"
)

func TestBrowserTargetIsTheStoreForChromiumAndTheXpiForFirefox(t *testing.T) {
	if got := browserTarget(winutil.Browser{Name: "Microsoft Edge", Chromium: true}, t.TempDir()); !strings.HasPrefix(got, "https://chromewebstore.google.com/detail/") {
		t.Errorf("Chromium target = %q", got)
	}
	if got := browserTarget(winutil.Browser{Name: "Firefox"}, t.TempDir()); !strings.HasSuffix(got, ".xpi") {
		t.Errorf("Firefox target = %q", got)
	}
}
