// Package atomicfile writes the engine's files so that a crash or power cut
// leaves either the old file or the new one, never an empty or half-written one.
package atomicfile

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// tmpSuffix marks a file being written; one left over is from an interrupted write.
const tmpSuffix = ".tmp"

// Write replaces path with data: a temp file next to it, flushed to disk, then
// renamed over it. The folder is created if needed.
func Write(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + tmpSuffix
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// WriteJSON writes v as indented JSON with Write.
func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return Write(path, b, 0o644)
}

// RemoveStale deletes temp files left below dir by interrupted writes.
func RemoveStale(dir string) {
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), tmpSuffix) {
			os.Remove(p)
		}
		return nil
	})
}
