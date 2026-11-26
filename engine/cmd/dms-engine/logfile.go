package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"downloadmanagersquared/engine/internal/logprivacy"
)

func setupLog(dir string) {
	l := &logFile{path: filepath.Join(dir, "engine.log"), max: 5 << 20, data: dir, level: logprivacy.Get}
	if l.open() != nil {
		return
	}
	log.SetOutput(l)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	// A panic's trace goes to stderr, which a GUI exe has nowhere: keep it in crash.log.
	if f, err := os.OpenFile(filepath.Join(dir, "crash.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintf(f, "\n=== engine started %s\n", time.Now().Format(time.RFC3339))
		if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
			log.Printf("crash.log: %v", err)
		}
		f.Close() // SetCrashOutput keeps its own handle
	}
}

// logFile is the engine's log: past max bytes it becomes <path>.old (replacing
// the last one) and a new file starts, also while the engine runs, which can
// be for weeks. The log package writes one line at a time, never two at once.
type logFile struct {
	path  string
	max   int64
	data  string                  // the engine's data folder, which scrub writes as %DATA%
	level func() logprivacy.Level // the setting Log privacy; nil: Standard
	f     *os.File
	size  int64

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

// Write scrubs each line of what is the user's own (scrub), and drops a line
// identical to one written less than dupWindow ago and
// counts it; a "(repeated N times)" line follows with the next line written
// after the window ends (shortcut: no timer, so the last count of a log that
// goes quiet waits for its next line).
func (l *logFile) Write(p []byte) (int, error) {
	n := len(p) // what the caller gave, whatever scrub made of it
	if len(p) > stampLen {
		p = append(p[:stampLen:stampLen], logprivacy.Scrub(string(p[stampLen:]), l.data, l.levelNow())...)
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
			return n, nil
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
	_, err := l.write(p)
	return n, err
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

// levelNow is the Log privacy level for the next line.
func (l *logFile) levelNow() logprivacy.Level {
	if l.level == nil {
		return logprivacy.Standard
	}
	return l.level()
}
