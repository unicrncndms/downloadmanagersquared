package download

import (
	"bytes"
	"slices"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/archive"
)

// Once a Download as folder has read its central directory, its plan cuts on
// the archive's Entry boundaries.
func TestAFolderDownloadsPlanKnowsItsEntryBoundaries(t *testing.T) {
	data := buildZip(t, manyFiles("", 48, 512<<10))
	a, err := archive.Read(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	want := entryBounds(a)
	srv := newZipServer(data)
	defer srv.Close()
	env := &testEnv{ifaces: []Iface{iface("eth", 2, 3*time.Millisecond)}, main: "eth", finished: make(chan struct{})}
	d := New(env, State{ID: "fb", URL: srv.URL + "/dl", Dir: t.TempDir(), Size: -1, Folder: &Folder{Auto: true, Into: t.TempDir()}})
	d.Start()
	var got []int64
	for got == nil {
		d.mu.Lock()
		if d.plan != nil {
			got = slices.Clone(d.plan.bounds)
		}
		d.mu.Unlock()
		select {
		case <-env.finished:
			if got == nil {
				t.Fatal("finished without the plan ever having Entry boundaries")
			}
		case <-time.After(time.Millisecond):
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("plan bounds %v, want %v", got, want)
	}
	<-env.finished
}
