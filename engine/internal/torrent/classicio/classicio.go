// Package classicio makes anacrolix/torrent's file storage use plain reads and
// writes instead of memory maps. Its mmap backend never unmaps a file, so on
// Windows a torrent's files would stay locked (no move, no delete) for as long
// as the engine runs.
//
// The storage package reads the choice once, in its own init, so this has to run
// first. Go initializes packages in import-path order among those whose imports
// are done; this one needs only os, and "downloadmanagersquared/..." sorts before
// "github.com/...", so it always does.
package classicio

import "os"

func init() {
	os.Setenv("TORRENT_STORAGE_DEFAULT_FILE_IO", "classic")
}
