// Package ui is the extension's pages that dms-engine.exe carries for its
// Downloads window: ui/... and lib/..., the layout the pages expect.
// build.ps1 copies them into files\ before it builds the engine and empties it
// again after; a plain `go build` or `go test` carries none, and Present says so.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:files
var embedded embed.FS

// Pages are the carried pages by path, e.g. "ui/popup.html".
var Pages, _ = fs.Sub(embedded, "files")

// The files without which the window has nothing to show.
var required = []string{"ui/popup.html", "ui/settings.html", "ui/host.js", "lib/version.js", "lib/folder.js", "lib/list.js", "lib/rows.js", "lib/iface.js"}

// Present says whether the exe carries the pages, i.e. was built by build.ps1.
func Present() bool { return present(Pages) }

func present(pages fs.FS) bool {
	for _, name := range required {
		if _, err := fs.Stat(pages, name); err != nil {
			return false
		}
	}
	return true
}
