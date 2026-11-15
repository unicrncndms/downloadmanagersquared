package queue

import (
	"fmt"
	"testing"

	"downloadmanagersquared/engine/internal/download"
)

// BenchmarkSave writes 20 dirty entries: one atomic file each.
func BenchmarkSave(b *testing.B) {
	q := New(b.TempDir(), nil)
	var ids []string
	for i := range 20 {
		f := &fake{id: fmt.Sprintf("d%d", i), status: download.StatusPaused}
		q.Add(f)
		ids = append(ids, f.id)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for _, id := range ids {
			q.MarkDirty(id)
		}
		q.Save()
	}
}
