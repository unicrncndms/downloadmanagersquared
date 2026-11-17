package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"time"

	"golang.org/x/sys/windows"

	"downloadmanagersquared/engine/internal/bundle"
	"downloadmanagersquared/engine/internal/engine"
	"downloadmanagersquared/engine/internal/install"
	"downloadmanagersquared/engine/internal/ipc"
	"downloadmanagersquared/engine/internal/setup"
	"downloadmanagersquared/engine/internal/tray"
	"downloadmanagersquared/engine/internal/winutil"
)

// putFiles is the part of an install that runs as administrator, started by
// the user's own install into a folder only an administrator can write to.
// It writes the files only: the registry belongs to the user installing.
func putFiles(src, dir string) {
	if !filepath.IsAbs(dir) {
		os.Exit(2)
	}
	l := install.Layout{Dir: filepath.Clean(dir), Icon: tray.Icon(), Bundled: bundle.Files}
	if err := l.PutFiles(src); err != nil {
		winutil.MessageBox(title, "Could not copy DMS² into "+l.Dir+": "+err.Error())
		os.Exit(1)
	}
}

// installFrom installs (first: what the user chose to set up) or, with first
// nil, upgrades from the folder this exe was started in, then starts the
// installed engine.
func installFrom(l install.Layout, src string, first *install.Options) error {
	if first == nil && !updateNow() {
		return winutil.ErrShown // later: nothing is touched
	}
	if !quitEngine() && len(winutil.ProcessesOf(l.Engine())) > 0 {
		// An engine from before the quit method and the per-user pipe.
		winutil.Dialog(title, "An older DMS² engine is running. Quit it from its tray icon (right-click → Quit), then press OK to update.",
			windows.MB_OK|windows.MB_ICONINFORMATION)
		for _, pid := range winutil.ProcessesOf(l.Engine()) {
			winutil.WaitExit(pid, 30*time.Second)
		}
	}
	if err := l.Install(src, engine.Version, first); err != nil {
		return err
	}
	if first != nil {
		return winutil.StartDetached(l.Engine(), flagInstalled) // setup's window says the rest
	}
	return winutil.StartDetached(l.Engine(), flagUpdated)
}

// uninstall removes the install after asking in one window, keeping the
// downloads.
func uninstall(l install.Layout) {
	data, _ := engine.DataDir()
	setup.RunUninstall(tray.Icon(), setup.Steps{
		Version:  engine.Version,
		Browsers: winutil.FirefoxBrowsers,
		OpenAddons: func(b winutil.Browser) {
			if err := winutil.OpenBrowser(b.Exe, "about:addons"); err != nil {
				winutil.MessageBox(title, "Could not open "+b.Name+": "+err.Error())
			}
		},
		Remove: func(wipe bool) error {
			if !quitEngine() {
				for _, pid := range winutil.ProcessesOf(l.Engine()) {
					winutil.Dialog(title, "Quit DMS² from its tray icon (right-click → Quit), then press OK.", windows.MB_OK|windows.MB_ICONINFORMATION)
					winutil.WaitExit(pid, 30*time.Second)
				}
			}
			errs := []error{l.Unregister()}
			// Started now, so Windows asks before the window says it is done; the
			// files go once this process, which runs from the folder, has exited.
			if err := l.RemoveLater(); err != nil {
				if errors.Is(err, winutil.ErrElevationDeclined) {
					// The files stay, so the extension and Windows' uninstall must still find them.
					err = errors.Join(err, l.Restore(engine.Version))
				}
				errs = append(errs, fmt.Errorf("could not remove %s: %w", l.Dir, err))
			}
			if wipe && data != "" {
				errs = append(errs, os.RemoveAll(data))
			}
			return errors.Join(errs...)
		},
	}, setup.UninstallInfo{DataDir: data, Unfinished: unfinishedFolders(data)})
}

// unfinishedFolders are the folders that hold an unfinished download's
// .dms.part file, from the saved download list in dataDir.
func unfinishedFolders(dataDir string) []string {
	files, _ := filepath.Glob(filepath.Join(dataDir, "downloads", "*.json"))
	var out []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var st struct{ Dir, Filename string }
		if json.Unmarshal(b, &st) != nil || st.Dir == "" || st.Filename == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(st.Dir, st.Filename+".dms.part")); err == nil && !slices.Contains(out, st.Dir) {
			out = append(out, st.Dir)
		}
	}
	slices.Sort(out)
	return out
}

// updateNow asks first when quitting the running engine would download a
// lot again. False: the user updates later. An engine that doesn't answer,
// or is too old to know the question, is updated.
func updateNow() bool {
	res, err := ipc.Call("restartCost")
	if err != nil {
		return true
	}
	var c engine.RestartCost
	if json.Unmarshal(res, &c) != nil {
		return true
	}
	w := c.Warning("Updating now")
	return w == "" || winutil.Dialog(title, w+"\n\nUpdate now? If you choose No, nothing is changed and you can update later.",
		windows.MB_YESNO|windows.MB_ICONWARNING|windows.MB_DEFBUTTON2) == winutil.IDYES
}

// quitEngine asks this user's running engine to quit and waits until it has
// exited, so its files can be replaced. False if none answered.
func quitEngine() bool {
	res, err := ipc.Call("quit")
	if err != nil {
		return false
	}
	var r struct {
		PID uint32 `json:"pid"`
	}
	json.Unmarshal(res, &r)
	if r.PID != 0 && !winutil.WaitExit(r.PID, 60*time.Second) {
		log.Printf("the engine didn't exit in time")
	}
	return true
}
