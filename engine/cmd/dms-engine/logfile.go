package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
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

	now  func() time.Time // the clock; time.Now unless a test sets it
	seen map[string]*repeat
}

// dupWindow is how long an identical line is written only once.
const dupWindow = time.Minute

// stampLen is the length of the log package's "2006/01/02 15:04:05.000000 " prefix.
const stampLen = 27

// repeat is a line written once, then counted when it comes again within dupWindow.
type repeat struct {
	first time.Time
	n     int
	msg   string
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

// Write drops a line identical to one written less than dupWindow ago and
// counts it; a "(repeated N times)" line follows with the next line written
// after the window ends (shortcut: no timer, so the last count of a log that
// goes quiet waits for its next line).
func (l *logFile) Write(p []byte) (int, error) {
	if len(p) > stampLen {
		now := time.Now()
		if l.now != nil {
			now = l.now()
		}
		if l.seen == nil {
			l.seen = map[string]*repeat{}
		}
		msg := string(p[stampLen:])
		if r := l.seen[msg]; r != nil && now.Sub(r.first) < dupWindow {
			r.n++
			return len(p), nil
		}
		for k, r := range l.seen {
			if now.Sub(r.first) >= dupWindow {
				delete(l.seen, k)
				if r.n > 0 {
					l.write([]byte(fmt.Sprintf("%s (repeated %d times) %s\n", now.Format("2006/01/02 15:04:05.000000"), r.n, strings.TrimSpace(r.msg))))
				}
			}
		}
		l.seen[msg] = &repeat{first: now, msg: msg}
	}
	return l.write(p)
}

func (l *logFile) write(p []byte) (int, error) {
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
