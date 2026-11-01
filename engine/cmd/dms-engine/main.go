// dms-engine is the background download engine with its tray icon.
// Build with -ldflags "-H windowsgui" so no console window appears.
package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"

	"downloadmanagersquared/engine/internal/engine"
	"downloadmanagersquared/engine/internal/ipc"
	"downloadmanagersquared/engine/internal/tray"
	"downloadmanagersquared/engine/internal/winutil"
)

func main() {
	e, err := engine.New()
	if errors.Is(err, engine.ErrAlreadyRunning) {
		// Launched twice (e.g. from the Start menu): bring up the downloads instead.
		if c, err := ipc.Dial(2 * time.Second); err == nil {
			b, _ := json.Marshal(ipc.Request{ID: 1, Method: "openManager"})
			c.Write(append(b, '\n'))
			c.Close()
		}
		return
	}
	if err != nil {
		winutil.MessageBox("downloadmanagersquared", "Could not start: "+err.Error())
		os.Exit(1)
	}
	setupLog(e.Dir())
	log.Printf("engine %s starting", engine.Version)
	go e.Run()
	tray.Run(e)
	log.Printf("engine stopped")
}

func setupLog(dir string) {
	path := filepath.Join(dir, "engine.log")
	if fi, err := os.Stat(path); err == nil && fi.Size() > 5<<20 {
		os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}
