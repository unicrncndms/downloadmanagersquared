// dms-window-dev is for checking the Downloads window by hand, beside an
// installed engine: it runs an engine of its own, kept in a folder of its own
// and reachable only from this process (not on the pipe), with its tray icon
// and the window open. The pages are read from the extension folder on disk,
// so a change to them shows on F5.
//
//	go run ./cmd/dms-window-dev [-dir folder] [-pages ../extension]
package main

import (
	"flag"
	"log"
	"os"

	"downloadmanagersquared/engine/internal/engine"
	"downloadmanagersquared/engine/internal/tray"
	"downloadmanagersquared/engine/internal/window"
)

func main() {
	dir := flag.String("dir", "", "the engine's data folder (default: a new temporary one)")
	pages := flag.String("pages", "../extension", "the folder holding ui/ and lib/")
	flag.Parse()
	if *dir == "" {
		d, err := os.MkdirTemp("", "dms-window-dev-")
		if err != nil {
			log.Fatal(err)
		}
		*dir = d
	}
	log.Printf("data in %s", *dir)
	e, err := engine.Open(*dir)
	if err != nil {
		log.Fatal(err)
	}
	w := window.New(window.Config{DataDir: *dir, Dial: e.Dial, Icon: tray.Icon(), Pages: os.DirFS(*pages)})
	go e.Run()
	go func() {
		if err := w.Open(""); err != nil {
			log.Printf("opening the window: %v", err)
		}
	}()
	tray.Run(e, w)
	log.Printf("stopped")
}
