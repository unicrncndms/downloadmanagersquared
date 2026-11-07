package engine

import (
	"context"
	"log"
	"net/http"
	"path/filepath"
	"time"

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
