package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogDedupe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.log")
	l := &logFile{path: path, max: 1 << 20}
	if err := l.open(); err != nil {
		t.Fatal(err)
	}
	defer l.f.Close()
	now := time.Now()
	l.now = func() time.Time { return now }
	line := func(s string) []byte { return []byte("2026/11/11 21:44:58.915655 " + s + "\n") }
	for range 10 {
		l.Write(line("a: failed"))
		l.Write(line("b: failed"))
		now = now.Add(200 * time.Millisecond)
	}
	now = now.Add(time.Minute)
	l.Write(line("c: other"))
	l.Write(line("a: failed"))
	b, _ := os.ReadFile(path)
	got := string(b)
	if n := strings.Count(got, "a: failed"); n != 3 { // first, summary, again after the window
		t.Errorf("a written %d times:\n%s", n, got)
	}
	if !strings.Contains(got, "(repeated 9 times) a: failed") || !strings.Contains(got, "(repeated 9 times) b: failed") {
		t.Errorf("counts missing:\n%s", got)
	}
}

// What reaches engine.log is scrubbed: no URL path, public address or file name.
func TestLogLinesAreScrubbed(t *testing.T) {
	dir := t.TempDir()
	l := &logFile{path: filepath.Join(dir, "engine.log"), max: 1 << 20, data: dir}
	if err := l.open(); err != nil {
		t.Fatal(err)
	}
	defer l.f.Close()
	in := "2026/11/11 21:44:58.915655 download ab: Get \"https://cdn.example.com/My.Movie.mkv?t=1\" via 95.90.208.111: rename " + `F:\x\My.Movie.mkv.dms.part F:\x\My.Movie.mkv` + ": denied\n"
	if n, err := l.Write([]byte(in)); err != nil || n != len(in) {
		t.Fatalf("wrote %d, %v; want %d", n, err, len(in))
	}
	b, _ := os.ReadFile(l.path)
	if got := string(b); strings.Contains(got, "Movie") || strings.Contains(got, "95.90") || !strings.Contains(got, "cdn.example.com") {
		t.Fatalf("not scrubbed: %s", got)
	}
}
