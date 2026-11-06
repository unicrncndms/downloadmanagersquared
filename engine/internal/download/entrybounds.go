package download

import "downloadmanagersquared/engine/internal/archive"

// entryBounds are the bytes where an archive's Entries start, and where its
// central directory does, in byte order: the plan's cut points for a
// Download as folder.
func entryBounds(a *archive.Archive) []int64 {
	b := make([]int64, 0, len(a.Entries)+1)
	for _, e := range a.Entries {
		b = append(b, e.Start)
	}
	return append(b, a.DirStart)
}
