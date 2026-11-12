package engine

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A release engine asks GitHub once a day and shows a newer release in its
// state; a restart keeps the answer without asking again.
func TestUpdateNoticeOncePerDay(t *testing.T) {
	var asked atomic.Int32
	latest := "v0.9.0"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		w.Write([]byte(`{"tag_name":"` + latest + `","html_url":"https://github.com/o/r/releases/tag/` + latest + `"}`))
	}))
	defer srv.Close()
	oldAPI, oldVersion := updateAPI, Version
	updateAPI = func() string { return srv.URL }
	Version = "0.8.0"
	defer func() { updateAPI, Version = oldAPI, oldVersion }()

	dir := t.TempDir()
	e, err := open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	waitChecked := func() {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			e.mu.Lock()
			done := !e.updChecking
			e.mu.Unlock()
			if done {
				return
			}
		}
		t.Fatal("the check didn't finish")
	}
	e.checkUpdate(now)
	waitChecked()
	e.checkUpdate(now.Add(time.Hour)) // not a day yet
	waitChecked()
	if n := asked.Load(); n != 1 {
		t.Fatalf("asked %d times, want 1", n)
	}
	e.updateSnapshot(time.Second)
	if u := e.Snapshot().Update; u == nil || u.Version != "0.9.0" {
		t.Fatalf("Update = %+v, want 0.9.0", u)
	}
	e.Shutdown()

	// After a restart: the notice stays, and GitHub isn't asked.
	e2, err := open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Shutdown()
	e2.checkUpdate(now.Add(2 * time.Hour))
	e2.updateSnapshot(time.Second)
	if u := e2.Snapshot().Update; u == nil || u.Version != "0.9.0" || asked.Load() != 1 {
		t.Fatalf("after restart: Update = %+v, asked %d", u, asked.Load())
	}

	// Once updated to that release, the notice goes.
	Version = "0.9.0"
	e2.updateSnapshot(time.Second)
	if u := e2.Snapshot().Update; u != nil {
		t.Fatalf("same version: Update = %+v, want none", u)
	}
}

// Development builds never ask GitHub.
func TestUpdateDevBuildNeverAsks(t *testing.T) {
	oldAPI := updateAPI
	updateAPI = func() string { t.Error("a dev build asked GitHub"); return "http://127.0.0.1:1" }
	defer func() { updateAPI = oldAPI }()
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	e.checkUpdate(time.Now())
	if e.updChecking {
		t.Fatal("a dev build started a check")
	}
}

// An update or Quit asks first only from 1 GiB to download again, and says
// how much and of what.
func TestARestartCostWarnsFrom1GiB(t *testing.T) {
	if w := (RestartCost{Bytes: 1<<30 - 1, Names: []string{"a.zip"}}).Warning("Updating now"); w != "" {
		t.Fatalf("under 1 GiB: %q", w)
	}
	w := RestartCost{Bytes: 3 << 30, Names: []string{"a.zip", "b.7z"}}.Warning("Updating now")
	if want := "Updating now means downloading 3.0 GiB again (a.zip, b.7z), because their extraction can't continue after a restart."; w != want {
		t.Fatalf("got %q, want %q", w, want)
	}
}
