package engine

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// addPaused adds an HTTP download with a landing page from x's browser, paused
// so it touches no network.
func addPaused(t *testing.T, e *Engine, x *fakeExtension) string {
	t.Helper()
	raw, _ := json.Marshal(AddRequest{URL: "https://files.test/a.iso", PageURL: "https://site.test/get/a"})
	res, err := e.Call(x.conn, "add", raw)
	if err != nil {
		t.Fatal(err)
	}
	id := res.(map[string]string)["id"]
	if err := e.queue.Pause(id); err != nil {
		t.Fatal(err)
	}
	return id
}

func recordOffered(t *testing.T, e *Engine, id string) bool {
	t.Helper()
	e.updateSnapshot(0)
	for _, s := range e.Snapshot().Downloads {
		if s.ID == id {
			return s.Actions.RecordRecipe
		}
	}
	t.Fatalf("%s is not listed", id)
	return false
}

func call(t *testing.T, e *Engine, x *fakeExtension, method string, params any) {
	t.Helper()
	raw, _ := json.Marshal(params)
	if _, err := e.Call(x.conn, method, raw); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
}

func TestRecordRecipeIsOfferedOnlyWhenTheBrowserHasNoRecipeForTheSite(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	firefox := fakeBrowserExe(t, "firefox.exe")
	ff := connectExtension(t, e, firefox, "Firefox UA")
	id := addPaused(t, e, ff)

	if recordOffered(t, e, id) {
		t.Fatal("offered before the extension said which sites have recipes")
	}
	call(t, e, ff, "recipeHosts", recipeHostsParams{Hosts: []string{"other.test"}})
	if !recordOffered(t, e, id) {
		t.Fatal("not offered for a site without a recipe")
	}
	call(t, e, ff, "recipeHosts", recipeHostsParams{Hosts: []string{"other.test", "site.test"}})
	if recordOffered(t, e, id) {
		t.Fatal("still offered once the site has a recipe")
	}
	call(t, e, ff, "recipeHosts", recipeHostsParams{Hosts: nil})
	e.Disconnected(ff.conn)
	if recordOffered(t, e, id) {
		t.Fatal("offered with the browser's extension not connected")
	}
}

func TestRecordRecipeIsNotOfferedWhileAResolveRuns(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	firefox := fakeBrowserExe(t, "firefox.exe")
	ff := connectExtension(t, e, firefox, "")
	id := addPaused(t, e, ff)
	call(t, e, ff, "recipeHosts", recipeHostsParams{})
	e.RequestResolve(id, "eth", "https://site.test/get/a", firefox)
	if recordOffered(t, e, id) {
		t.Fatal("offered while a resolve runs")
	}
	call(t, e, ff, "resolveFailed", resolvedParams{ID: id, Iface: "eth", Error: "x"})
	if !recordOffered(t, e, id) {
		t.Fatal("not offered once the resolve ended")
	}
}

func TestRecordRecipeSendsALearnResolveAndLeavesTheLinksAlone(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	firefox := fakeBrowserExe(t, "firefox.exe")
	ff := connectExtension(t, e, firefox, "")
	id := addPaused(t, e, ff)
	call(t, e, ff, "recipeHosts", recipeHostsParams{})
	d, err := e.get(id)
	if err != nil {
		t.Fatal(err)
	}
	before := d.State().Links

	call(t, e, ff, "recordRecipe", idParams{ID: id})
	select {
	case raw := <-ff.resolves:
		var r resolveReq
		json.Unmarshal(raw, &r)
		if !r.Learn || r.ID != id || r.PageURL != "https://site.test/get/a" {
			t.Fatalf("resolve = %s, want a learn resolve of the landing page", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the extension got no resolve")
	}
	if after := d.State().Links; !reflect.DeepEqual(before, after) {
		t.Fatalf("links changed: %v -> %v", before, after)
	}
	if len(e.resolves) != 0 {
		t.Fatal("a learn resolve is not a pending resolve")
	}
	if recordOffered(t, e, id) {
		t.Fatal("offered while the recording runs")
	}
	call(t, e, ff, "learnEnded", idParams{ID: id})
	if !recordOffered(t, e, id) {
		t.Fatal("not offered again after a recording that saved no recipe")
	}
}

func TestRecordRecipeNeedsTheBrowserRunning(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	ff := connectExtension(t, e, fakeBrowserExe(t, "firefox.exe"), "")
	id := addPaused(t, e, ff)
	e.Disconnected(ff.conn)
	raw, _ := json.Marshal(idParams{ID: id})
	if _, err := e.Call(ff.conn, "recordRecipe", raw); err == nil {
		t.Fatal("recordRecipe worked without the browser's extension")
	}
}
