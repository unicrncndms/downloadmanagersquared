package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"downloadmanagersquared/engine/internal/atomicfile"
	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/queue"
	"downloadmanagersquared/engine/internal/torrent"
)

// loadDownloads restores both kinds, in the order they were added. Unreadable
// state files are set aside, with one balloon for all of them.
func (e *Engine) loadDownloads() {
	var all []queue.Entry
	states, broken := readStates[download.State](filepath.Join(e.dir, "downloads"))
	for _, st := range states {
		if st.Status == download.StatusRunning || st.Status == download.StatusVerifying {
			st.Status = download.StatusQueued
		}
		st.VPN = paths.ParseMode(string(st.VPN), paths.Off) // saved before modes were spelled out
		all = append(all, download.New(e, st))
	}
	tstates, tbroken := readStates[torrent.State](filepath.Join(e.dir, "torrents"))
	if n := broken + tbroken; n == 1 {
		notify("A download couldn't be restored", "Its unfinished file ends in .dms.part and can be deleted.", nil)
	} else if n > 1 {
		notify("Downloads couldn't be restored", fmt.Sprintf("%d downloads couldn't be restored. Their unfinished files end in .dms.part and can be deleted.", n), nil)
	}
	for _, st := range tstates {
		// Seeding and fetching metadata go on (schedule restarts them); the rest queue again.
		switch st.Status {
		case download.StatusRunning, torrent.StatusChecking:
			st.Status = download.StatusQueued
		}
		st.VPN = paths.ParseMode(string(st.VPN), paths.Off)
		t, err := e.session(st.VPN).Add(st)
		if err != nil {
			log.Printf("skipping torrent %s: %v", st.ID, err)
			continue
		}
		all = append(all, t)
	}
	e.queue = queue.New(e.dir, all)
	for _, st := range states {
		if st.HasPlainCookies() {
			e.queue.MarkDirty(st.ID) // saved encrypted at the next tick, not left readable
		}
	}
}

// readStates reads every saved state in dir that has an ID. The others are
// set aside as <id>.json.broken; broken counts them.
func readStates[S interface{ download.State | torrent.State }](dir string) (out []S, broken int) {
	older, _ := filepath.Glob(filepath.Join(dir, "*.json.broken"))
	for _, f := range older { // set aside by a version that kept their cookies
		if b, err := os.ReadFile(f); err == nil {
			if clean := withoutCookies(b); !bytes.Equal(clean, b) {
				if err := atomicfile.Write(f, clean, 0o600); err != nil {
					log.Printf("removing cookies from %s: %v", f, err)
				}
			}
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		var st S
		var id struct {
			ID string `json:"id"`
		}
		b, err := os.ReadFile(f)
		if err != nil || json.Unmarshal(b, &st) != nil || json.Unmarshal(b, &id) != nil || id.ID == "" {
			log.Printf("unreadable state %s, set aside", f)
			setAsideWithoutCookies(f, b)
			broken++
			continue
		}
		out = append(out, st)
	}
	return out, broken
}
