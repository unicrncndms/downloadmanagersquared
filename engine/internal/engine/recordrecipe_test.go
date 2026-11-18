package engine

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/resolver"
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

func TestRecordRecipeIsOfferedOnlyWhenTheSiteHasNoRecipe(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	firefox := fakeBrowserExe(t, "firefox.exe")
	ff := connectExtension(t, e, firefox, "Firefox UA")
	id := addPaused(t, e, ff)

	if !recordOffered(t, e, id) {
		t.Fatal("not offered for a site without a recipe")
	}
	call(t, e, ff, "setRecipe", recipeParams{Host: "site.test", Recipe: &resolver.Recipe{Learned: 1}})
	if recordOffered(t, e, id) {
		t.Fatal("still offered once the site has a recipe")
	}
	call(t, e, ff, "forgetRecipe", recipeParams{Host: "site.test"})
	if !recordOffered(t, e, id) {
		t.Fatal("not offered again once the recipe is forgotten")
	}
	e.Disconnected(ff.conn)
	if recordOffered(t, e, id) {
		t.Fatal("offered with the browser's extension not connected")
	}
}

// One store for every browser: a recipe a browser sends is kept with its name,
// every client hears of the change, and a browser's old recipes merge in with
// the newer one winning per host.
func TestRecipesAreKeptByTheEngineForEveryBrowser(t *testing.T) {
	dir := t.TempDir()
	e, err := open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	ff := connectExtension(t, e, fakeBrowserExe(t, "firefox.exe"), "")
	chrome := connectExtension(t, e, fakeBrowserExe(t, "chrome.exe"), "")

	call(t, e, ff, "setRecipe", recipeParams{Host: "a.test", Recipe: &resolver.Recipe{Steps: []resolver.Step{{Tag: "a", Text: "Go"}}, Learned: 100}})
	if !chrome.next("recipes", 2*time.Second) {
		t.Fatal("another browser didn't hear of the new recipe")
	}
	if r := e.recipes.Get("a.test"); r == nil || r.From != "firefox" {
		t.Fatalf("recipe %+v, want it marked as learned in firefox", r)
	}
	call(t, e, chrome, "mergeRecipes", mergeRecipesParams{Recipes: map[string]*resolver.Recipe{
		"a.test": {Learned: 50},  // older: the one kept stays
		"b.test": {Learned: 200}, // new
	}})
	if r := e.recipes.Get("a.test"); r.Learned != 100 {
		t.Fatal("an older recipe replaced a newer one")
	}
	if r := e.recipes.Get("b.test"); r == nil || r.From != "chrome" {
		t.Fatalf("merged recipe %+v", r)
	}
	got, err := e.Call(nil, "recipes", nil)
	if err != nil || len(got.(map[string]*resolver.Recipe)) != 2 {
		t.Fatalf("recipes = %v, %v", got, err)
	}
	e.Shutdown()
	e2, err := open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Shutdown()
	if e2.recipes.Get("b.test") == nil {
		t.Fatal("recipes lost across a restart")
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
