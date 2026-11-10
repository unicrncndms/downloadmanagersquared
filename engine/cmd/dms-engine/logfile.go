package main

import (
	"log"
	"os"
	"path/filepath"
)

func setupLog(dir string) {
	l := &logFile{path: filepath.Join(dir, "engine.log"), max: 5 << 20}
	if l.open() != nil {
		return
	}
	log.SetOutput(l)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}

// logFile is the engine's log: past max bytes it becomes <path>.old (replacing
// the last one) and a new file starts, also while the engine runs, which can
// be for weeks. The log package writes one line at a time, never two at once.
type logFile struct {
	path string
	max  int64
	f    *os.File
	size int64
}

func (l *logFile) open() error {
	if fi, err := os.Stat(l.path); err == nil && fi.Size() > l.max {
		os.Rename(l.path, l.path+".old")
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	l.f, l.size = f, 0
	if fi, err := f.Stat(); err == nil {
		l.size = fi.Size()
	}
	return nil
}

func (l *logFile) Write(p []byte) (int, error) {
	if l.size > l.max {
		l.f.Close()
		if err := l.open(); err != nil {
			return 0, err // nowhere to log; the next line tries again
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}
