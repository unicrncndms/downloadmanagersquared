// Package bundle is what dms-engine.exe carries so it can install on its own:
// dms-host.exe and the licence files. build.ps1 puts them in files\ before it
// builds the engine and empties it again after; a plain `go build` carries
// nothing, and the install then takes the files next to the exe.
package bundle

import (
	"embed"
	"io/fs"
)

//go:embed all:files
var embedded embed.FS

// Files are the carried files by name, e.g. "dms-host.exe".
var Files, _ = fs.Sub(embedded, "files")
