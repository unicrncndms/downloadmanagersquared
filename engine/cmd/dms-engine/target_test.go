package main

import (
	"strings"
	"testing"

	"downloadmanagersquared/engine/internal/winutil"
)

func TestBrowserTargetIsTheStoreForChromiumAndAMOForFirefox(t *testing.T) {
	if got := browserTarget(winutil.Browser{Name: "Microsoft Edge", Chromium: true}); !strings.HasPrefix(got, "https://chromewebstore.google.com/detail/") {
		t.Errorf("Chromium target = %q", got)
	}
	if got := browserTarget(winutil.Browser{Name: "Firefox"}); got != "https://addons.mozilla.org/firefox/addon/downloadmanagersquared/" {
		t.Errorf("Firefox target = %q", got)
	}
}
