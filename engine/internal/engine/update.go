package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/install"
	"downloadmanagersquared/engine/internal/update"
	"downloadmanagersquared/engine/internal/winutil"
)

// UpdateView is a newer release than this engine, shown as a notice.
type UpdateView struct {
	Version    string `json:"version"`
	URL        string `json:"url"`
	Installing bool   `json:"installing,omitempty"` // the update is being downloaded
	Error      string `json:"error,omitempty"`      // why the last try failed
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
	return &UpdateView{Version: v, URL: url, Installing: e.updInstall, Error: e.updErr}
}

var errDevBuild = userErr("This is a development build, which does not check for updates.")

// checkUpdateNow asks GitHub now, whatever the last check was, and answers
// the notice (nil: this engine is the newest).
func (e *Engine) checkUpdateNow() (*UpdateView, error) {
	if !update.Release(Version) {
		return nil, errDevBuild
	}
	e.mu.Lock()
	if e.updChecking {
		e.mu.Unlock()
		return nil, userErr("A check is already running. Try again in a moment.")
	}
	e.updChecking = true
	e.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	v, url, err := update.Fetch(ctx, &http.Client{Timeout: 30 * time.Second}, updateAPI())
	e.mu.Lock()
	defer e.mu.Unlock()
	e.updChecking = false
	if err != nil {
		log.Printf("checking for an update: %v", err)
		return nil, userErr("Could not reach GitHub to look for an update.")
	}
	e.upd.Checked, e.upd.Latest, e.upd.URL = time.Now(), v, url
	if err := e.upd.Save(filepath.Join(e.dir, "update.json")); err != nil {
		log.Printf("saving the update check: %v", err)
	}
	e.requestPush()
	return e.updateViewLocked(), nil
}

// installUpdate downloads the latest release in the background and starts its
// engine, which upgrades this install by the normal path (it asks about
// restart cost and has this engine quit). Progress and failure show in the
// state's update notice.
func (e *Engine) installUpdate() error {
	if !update.Release(Version) {
		return errDevBuild
	}
	e.mu.Lock()
	if e.updInstall {
		e.mu.Unlock()
		return userErr("An update is already being downloaded.")
	}
	e.updInstall, e.updErr = true, ""
	e.mu.Unlock()
	e.requestPush()
	go func() {
		err := e.downloadAndStart()
		e.mu.Lock()
		e.updInstall = false
		if err != nil {
			log.Printf("installing the update: %v", err)
			e.updErr = err.Error()
		}
		e.mu.Unlock()
		e.requestPush()
	}()
	return nil
}

func (e *Engine) downloadAndStart() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client := &http.Client{}
	l, err := update.FetchLatest(ctx, client, updateAPI())
	if err != nil {
		return userErr("Could not reach GitHub to find the update.")
	}
	if !update.Newer(l.Version, Version) {
		return userErr("You already have the newest version.")
	}
	if l.ZipURL == "" {
		return userErr("The release has no engine download.")
	}
	exe, err := update.Download(ctx, client, l.ZipURL, l.Digest)
	if err != nil {
		return err
	}
	if err := winutil.StartDetached(exe); err != nil {
		os.RemoveAll(filepath.Dir(exe))
		return userErr("Could not start the downloaded update: " + err.Error())
	}
	return nil
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

// userErr is an error whose sentence is shown to the user as is, so it
// starts with a capital.
func userErr(s string) error { return errors.New(s) }
