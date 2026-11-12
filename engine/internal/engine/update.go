package engine

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/install"
	"downloadmanagersquared/engine/internal/update"
)

// UpdateView is a newer release than this engine, shown as a notice.
type UpdateView struct {
	Version string `json:"version"`
	URL     string `json:"url"`
}

// updateAPI is GitHub's answer for the latest release (tests replace it).
var updateAPI = func() string { return "https://api.github.com/repos/" + install.Repo + "/releases/latest" }

// checkUpdate asks GitHub for the latest release when a day has passed since
// the last time, in the background. A failure is logged and waits for the
// next day like a success. Development builds never ask.
func (e *Engine) checkUpdate(now time.Time) {
	if !update.Release(Version) {
		return
	}
	e.mu.Lock()
	if e.updChecking || !e.upd.Due(now) {
		e.mu.Unlock()
		return
	}
	e.updChecking = true
	e.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		v, url, err := update.Fetch(ctx, &http.Client{Timeout: 30 * time.Second}, updateAPI())
		e.mu.Lock()
		defer e.mu.Unlock()
		e.updChecking = false
		e.upd.Checked = now
		if err != nil {
			log.Printf("checking for an update: %v", err)
		} else {
			e.upd.Latest, e.upd.URL = v, url
		}
		if err := e.upd.Save(filepath.Join(e.dir, "update.json")); err != nil {
			log.Printf("saving the update check: %v", err)
		}
	}()
}

// updateViewLocked is the notice for the state, or nil when this engine is
// the newest. Called with e.mu held.
func (e *Engine) updateViewLocked() *UpdateView {
	v, url, ok := e.upd.Available(Version)
	if !ok {
		return nil
	}
	return &UpdateView{Version: v, URL: url}
}

// RestartCost is what stopping the engine now would download again: the bytes
// freed of the large Entries being extracted as they download, whose
// decompressors live only in memory (a restart extracts each again from its
// front), and the names of their archives.
type RestartCost struct {
	Bytes int64    `json:"bytes"`
	Names []string `json:"names,omitempty"`
}

// restartWarn is the cost from which an update or Quit asks first.
const restartWarn = 1 << 30

func (e *Engine) RestartCost() RestartCost {
	var c RestartCost
	add := func(n int64, name string) {
		if n > 0 {
			c.Bytes += n
			c.Names = append(c.Names, name)
		}
	}
	for _, j := range e.queue.List() {
		if d, ok := j.(*download.Download); ok {
			add(d.RestartCost(), d.Summary().Filename)
		}
	}
	e.setsMu.Lock()
	for _, s := range e.sets {
		add(s.RestartCost(), s.Name())
	}
	e.setsMu.Unlock()
	return c
}

// Warning says what doing ("Updating now") would cost, or is "" when that is
// too little to ask about.
func (c RestartCost) Warning(doing string) string {
	if c.Bytes < restartWarn {
		return ""
	}
	return fmt.Sprintf("%s means downloading %s again (%s), because their extraction can't continue after a restart.",
		doing, download.Human(c.Bytes), strings.Join(c.Names, ", "))
}
