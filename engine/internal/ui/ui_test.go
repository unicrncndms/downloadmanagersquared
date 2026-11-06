package ui

import (
	"io/fs"
	"testing"
	"testing/fstest"
)

func TestPresentWithoutTheCopy(t *testing.T) {
	empty := fstest.MapFS{".gitkeep": {}}
	if present(empty) {
		t.Fatal("present with only .gitkeep")
	}
}

func TestPresentWithThePages(t *testing.T) {
	pages := fstest.MapFS{
		"ui/popup.html":    {Data: []byte("<!doctype html>")},
		"ui/settings.html": {Data: []byte("<!doctype html>")},
		"ui/host.js":       {Data: []byte("")},
		"lib/version.js":   {Data: []byte("")},
		"lib/folder.js":    {Data: []byte("")},
	}
	if !present(pages) {
		t.Fatal("not present with the pages")
	}
}

func TestPresentWithoutAPage(t *testing.T) {
	half := fstest.MapFS{"ui/popup.html": {}, "lib/version.js": {}}
	if present(half) {
		t.Fatal("present without settings.html")
	}
}

// The embedded pages on whatever checkout runs the test: on a plain
// `go test ./...` that is just .gitkeep, and nothing may panic.
func TestEmbeddedPages(t *testing.T) {
	if Pages == nil {
		t.Fatal("Pages is nil")
	}
	_, err := fs.Stat(Pages, "ui/popup.html")
	if got, want := Present(), err == nil; got != want {
		t.Fatalf("Present() = %v, popup.html there = %v", got, want)
	}
}
