package download

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"downloadmanagersquared/engine/internal/archive"
	"downloadmanagersquared/engine/internal/winutil"
)

// fakePart is a part with all its bytes on disk, as an Extraction sees it,
// with no download behind it: it records what the Extraction asks of it.
type fakePart struct {
	mu        sync.Mutex
	f         Folder
	size      int64
	prefer    int64
	refetched []Range
	paused    string
	saves     int
	path      string // its file
}

func (p *fakePart) partInfo() partInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.f
	return partInfo{on: f.On, into: f.Into, out: f.Out, size: p.size, probed: true, allocated: true,
		freed: slices.Clone(f.Freed), done: slices.Clone(f.Done), password: string(f.Password), ask: f.Ask, files: f.Files, of: f.Of,
		keep: f.keepNow(KeepFree), auto: f.keepChoice(KeepFree) == KeepAuto, path: p.path}
}

func (p *fakePart) editFolder(edit func(f *Folder) bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	edit(&p.f)
}

func (p *fakePart) preferTail(at int64) { p.prefer = at }

func (p *fakePart) refetch(r Range) {
	p.refetched = append(p.refetched, r)
	p.f.Freed = subtractRanges(p.f.Freed, []Range{r})
}

func (p *fakePart) pause(msg string) { p.paused = msg }

func (p *fakePart) save() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.saves++
	return nil
}

// fakeExtraction lays data over n parts, each a sparse file of its own, as a
// Set does; one part is read as a download's own, without archive.Parts.
func fakeExtraction(t *testing.T, data []byte, n int) (*Extraction, []archivePart, []*fakePart) {
	t.Helper()
	dir, into := t.TempDir(), t.TempDir()
	var sizes []int64
	for left, each := int64(len(data)), (int64(len(data))+int64(n)-1)/int64(n); left > 0; left -= each {
		sizes = append(sizes, min(left, each))
	}
	layout := archive.NewParts(sizes)
	var fs []*fakePart
	var ps []archivePart
	var file archive.File = layout
	for i, size := range sizes {
		span := layout.Span(i)
		f, err := os.Create(filepath.Join(dir, filepath.Base(t.Name())+string(rune('a'+i))))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		winutil.SetSparse(f, true)
		if _, err := f.Write(data[span.Start:span.End]); err != nil {
			t.Fatal(err)
		}
		layout.Set(i, archive.Sparse(f))
		if n == 1 {
			file = archive.Sparse(f)
		}
		p := &fakePart{f: Folder{On: true, Into: into}, size: size, prefer: -1, path: f.Name()}
		fs, ps = append(fs, p), append(ps, p)
	}
	e := &Extraction{name: "big.zip", who: "test", file: file, parts: layout, head: archive.HeadLen, mark: func(string) {}, tail: -1}
	t.Cleanup(e.close)
	return e, ps, fs
}

// stepUntil steps e with every byte on disk until done says so.
func stepUntil(t *testing.T, e *Extraction, ps []archivePart, what string, done func() bool) {
	t.Helper()
	for range 100 {
		if _, err := e.step(context.Background(), ps, nil); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if done() {
			return
		}
	}
	t.Fatalf("never %s", what)
}

// An Entry that comes out wrong is fetched again on the parts holding it and
// remembered on part 1; wrong again, those parts pause and the others don't.
func TestAnExtractionsWrongEntryIsFetchedAgainThenPauses(t *testing.T) {
	for _, n := range []int{1, 3} {
		data := buildZip(t, manyFiles("", 8, 256<<10))
		data[len(data)/2] ^= 0xff
		e, ps, fs := fakeExtraction(t, data, n)
		stepUntil(t, e, ps, "failed once", func() bool { return len(fs[0].f.Failed) == 1 })
		var holding []int
		for i, p := range fs {
			if len(p.refetched) > 0 {
				holding = append(holding, i)
			}
			if p.paused != "" {
				t.Fatalf("%d parts: part %d paused after one failure", n, i+1)
			}
		}
		if len(holding) == 0 || n == 3 && len(holding) == n {
			t.Fatalf("%d parts: fetched again on parts %v", n, holding)
		}
		stepUntil(t, e, ps, "paused", func() bool { return fs[holding[0]].paused != "" })
		for i, p := range fs {
			if (p.paused != "") != slices.Contains(holding, i) {
				t.Errorf("%d parts: part %d paused %q, holding %v", n, i+1, p.paused, holding)
			}
		}
		if len(fs[0].f.Failed) != 0 {
			t.Errorf("%d parts: still failed %v: its next try gets another fetch first", n, fs[0].f.Failed)
		}
	}
}

// The Entries done and the count of files are on part 1, the bytes freed on
// the part they are in, in its own offsets, saved before they are freed; the
// list of files is fetched first.
func TestAnExtractionRecordsOnTheRightPart(t *testing.T) {
	files := manyFiles("", 8, 256<<10)
	e, ps, fs := fakeExtraction(t, buildZip(t, files), 3)
	total, _ := e.parts.Size()
	if _, err := e.step(context.Background(), ps, []archive.Range{{Start: 0, End: total}}); err != nil {
		t.Fatal(err)
	}
	if last := e.parts.Span(2); e.tail != 2 || fs[2].prefer != total-archive.TailGuess-last.Start {
		t.Fatalf("waits for part %d, the last part prefers %d", e.tail+1, fs[2].prefer)
	}
	stepUntil(t, e, ps, "finished", func() bool { return e.finished })
	if p := fs[0].f; len(p.Done) != 8 || p.Files != 8 || p.Of != 8 || p.Out == "" {
		t.Fatalf("part 1: %+v", p)
	}
	checkTree(t, fs[0].f.Out, files, "")
	for i, p := range fs {
		if i > 0 && (len(p.f.Done) > 0 || p.f.Out != "") {
			t.Errorf("part %d holds the record: %+v", i+1, p.f)
		}
		if len(p.f.Freed) == 0 || p.saves == 0 {
			t.Errorf("part %d: nothing freed, or not saved (%d saves)", i+1, p.saves)
		}
		for _, r := range p.f.Freed {
			if r.Start < 0 || r.End > p.size {
				t.Errorf("part %d of %d bytes: freed %v", i+1, p.size, r)
			}
		}
	}
}

// An encrypted archive waits for part 1's password, asks again for a wrong
// one, and forgets the right one once it is all extracted.
func TestAnExtractionAsksForThePassword(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "archive", "testdata", "password.7z"))
	if err != nil {
		t.Fatal(err)
	}
	e, ps, fs := fakeExtraction(t, data, 1)
	p := fs[0]
	stepUntil(t, e, ps, "asked", func() bool { return p.f.Ask == AskPassword })
	if !e.asking || e.arc != nil {
		t.Fatalf("asking %v, list read %v", e.asking, e.arc != nil)
	}
	p.f.Password, p.f.Ask = "wrong", ""
	stepUntil(t, e, ps, "asked again", func() bool { return p.f.Ask == AskWrong })
	p.f.Password, p.f.Ask = "secret", ""
	stepUntil(t, e, ps, "finished", func() bool { return e.finished })
	if p.f.Password != "" || p.f.Files == 0 || p.f.Files != p.f.Of {
		t.Fatalf("part 1: %+v", p.f)
	}
}

// An archive it can't extract as it downloads says so, before anything is
// named or freed, for its owner to download it as it is.
func TestAnExtractionOfAnUnsupportedArchiveSaysSo(t *testing.T) {
	files := manyFiles("", 4, 64<<10)
	files[2].flags = 1 // encrypted, which a ZIP can't be here
	e, ps, fs := fakeExtraction(t, buildZip(t, files), 1)
	var err error
	for range 10 {
		if _, err = e.step(context.Background(), ps, nil); err != nil {
			break
		}
	}
	var u *archive.Unsupported
	if !errors.As(err, &u) || e.arc != nil {
		t.Fatalf("err %v, list read %v", err, e.arc != nil)
	}
	if f := fs[0].f; f.Out != "" || len(f.Freed) > 0 || len(f.Done) > 0 {
		t.Fatalf("part 1 changed: %+v", f)
	}
}
