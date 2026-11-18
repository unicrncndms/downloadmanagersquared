package cdp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestARealBrowser starts the browser named by DMS_CDP_EXE (skipped without
// it), opens a page in a context of its own over flat sessions, and closes it.
func TestARealBrowser(t *testing.T) {
	exe := os.Getenv("DMS_CDP_EXE")
	if exe == "" {
		t.Skip("set DMS_CDP_EXE to a chrome.exe or msedge.exe")
	}
	attached := make(chan string, 8)
	b, err := Start(exe, t.TempDir(), func(e Event) {
		if e.Method == "Target.attachedToTarget" {
			attached <- string(e.Params)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	v, err := b.Call(ctx, "", "Browser.getVersion", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("version %s", v)
	if os.Getenv("DMS_CDP_HOLD") != "" {
		time.Sleep(time.Minute) // for killing the test: does the browser quit with it?
	}
	r, err := b.Call(ctx, "", "Target.createBrowserContext", map[string]any{"disposeOnDetach": true})
	if err != nil {
		t.Fatal(err)
	}
	var bc struct{ BrowserContextID string }
	jsonInto(t, r, &bc)
	r, err = b.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank", "browserContextId": bc.BrowserContextID, "newWindow": true})
	if err != nil {
		t.Fatal(err)
	}
	var tg struct{ TargetID string }
	jsonInto(t, r, &tg)
	r, err = b.Call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": tg.TargetID, "flatten": true})
	if err != nil {
		t.Fatal(err)
	}
	var s struct{ SessionID string }
	jsonInto(t, r, &s)
	select {
	case p := <-attached:
		if !strings.Contains(p, s.SessionID) {
			t.Fatalf("attached event %s, session %s", p, s.SessionID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no Target.attachedToTarget event")
	}
	r, err = b.Call(ctx, s.SessionID, "Runtime.evaluate", map[string]any{"expression": "6*7", "returnByValue": true})
	if err != nil || !strings.Contains(string(r), "42") {
		t.Fatalf("evaluate: %s %v", r, err)
	}
	start := time.Now()
	b.Close()
	t.Logf("closed in %v", time.Since(start))
	if _, err := b.Call(ctx, "", "Browser.getVersion", nil); err == nil {
		t.Fatal("a call after Close worked")
	}
}

func jsonInto(t *testing.T, r json.RawMessage, v any) {
	t.Helper()
	if err := json.Unmarshal(r, v); err != nil {
		t.Fatal(err)
	}
}
