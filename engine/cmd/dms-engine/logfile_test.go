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
