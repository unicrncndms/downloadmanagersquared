// dms-engine is the background download engine with its tray icon. It also
// installs itself: started from anywhere but the install folder, it asks
// where to install (or upgrades the install there), copies itself there and
// starts that copy instead.
// Build with -ldflags "-H windowsgui" so no console window appears.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"time"

	"golang.org/x/sys/windows"

	"downloadmanagersquared/engine/internal/engine"
	"downloadmanagersquared/engine/internal/install"
	"downloadmanagersquared/engine/internal/ipc"
	"downloadmanagersquared/engine/internal/setup"
	"downloadmanagersquared/engine/internal/tray"
	"downloadmanagersquared/engine/internal/winutil"
)

const title = "downloadmanagersquared"

// Flags the installing copy passes to the installed one.
const (
	flagUpdated   = "--updated"   // upgrade: say so in a balloon
	flagUninstall = "--uninstall" // from Installed apps
)

func main() {
	self, err := os.Executable()
	if err != nil {
		fail(err)
	}
	if len(os.Args) == 3 && os.Args[1] == install.FlagPutFiles {
		putFiles(filepath.Dir(self), os.Args[2])
		return
	}
	l, err := install.Default()
	if err != nil {
		fail(err)
	}
	l.Icon = tray.Icon()
	l, installed := l.Find()
	if slices.Contains(os.Args[1:], flagUninstall) {
		uninstall(l)
		return
	}
	if !l.Contains(self) {
		src := filepath.Dir(self)
		if installed { // an upgrade goes where the install is, without asking
			if err := installFrom(l, src, nil); err != nil {
				if !errors.Is(err, install.ErrShown) {
					winutil.MessageBox(title, "Could not install: "+err.Error())
				}
				os.Exit(1)
			}
			return
		}
		setup.Run(setup.Choice{Dir: l.Dir, StartMenu: true, Autostart: true}, tray.Icon(), setup.Steps{
			Install: func(c setup.Choice) error {
				l.Dir = c.Dir
				err := installFrom(l, src, &install.Options{StartMenu: c.StartMenu, Desktop: c.Desktop, Autostart: c.Autostart})
				if errors.Is(err, install.ErrShown) {
					return setup.ErrReported
				}
				return err
			},
			AddExtension: func() {
				if err := winutil.OpenInFirefox(install.ExtensionURL()); err != nil {
					winutil.MessageBox(title, "Could not open Firefox: "+err.Error())
				}
			},
			ExtensionURL: install.ExtensionURL(),
		})
		return
	}
	run(l)
}

// putFiles is the part of an install that runs as administrator, started by
// the user's own install into a folder only an administrator can write to.
// It writes the files only: the registry belongs to the user installing.
func putFiles(src, dir string) {
	if !filepath.IsAbs(dir) {
		os.Exit(2)
	}
	l := install.Layout{Dir: filepath.Clean(dir), Icon: tray.Icon()}
	if err := l.PutFiles(src); err != nil {
		winutil.MessageBox(title, "Could not copy DMS² into "+l.Dir+": "+err.Error())
		os.Exit(1)
	}
}

// run is the installed engine.
func run(l install.Layout) {
	e, err := engine.New()
	if errors.Is(err, engine.ErrAlreadyRunning) {
		// Launched twice (e.g. from the Start menu): bring up the downloads instead.
		call("openManager")
		return
	}
	if err != nil {
		fail(err)
	}
	setupLog(e.Dir())
	log.Printf("engine %s starting", engine.Version)
	if err := l.Register(); err != nil { // a browser can't start us without it
		log.Printf("registering the native host: %v", err)
	}
	if slices.Contains(os.Args[1:], flagUpdated) {
		winutil.Notify("Updated", "DMS² is updated to v"+engine.Version+".")
	}
	go e.Run()
	tray.Run(e)
	log.Printf("engine stopped")
}

// installFrom installs (first: what the user chose to set up) or, with first
// nil, upgrades from the folder this exe was started in, then starts the
// installed engine.
func installFrom(l install.Layout, src string, first *install.Options) error {
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
		return winutil.StartDetached(l.Engine()) // setup's window says the rest
	}
	return winutil.StartDetached(l.Engine(), flagUpdated)
}

// uninstall removes the install after asking, keeping the downloads.
func uninstall(l install.Layout) {
	if winutil.Dialog(title, "Uninstall downloadmanagersquared?\n\nDownloaded files are kept, including unfinished ones.",
		windows.MB_OKCANCEL|windows.MB_ICONQUESTION) != winutil.IDOK {
		return
	}
	data, _ := engine.DataDir()
	wipe := winutil.Dialog(title, "Also delete settings and download history?\n\n"+
		"That is everything in "+data+": settings, learned sites, the download list, VPN keys and torrents. "+
		"Downloaded files are kept either way.",
		windows.MB_YESNO|windows.MB_DEFBUTTON2|windows.MB_ICONQUESTION) == winutil.IDYES
	if !quitEngine() {
		for _, pid := range winutil.ProcessesOf(l.Engine()) {
			winutil.Dialog(title, "Quit DMS² from its tray icon (right-click → Quit), then press OK.", windows.MB_OK|windows.MB_ICONINFORMATION)
			winutil.WaitExit(pid, 30*time.Second)
		}
	}
	var errs []error
	errs = append(errs, l.Unregister())
	if wipe && data != "" {
		errs = append(errs, os.RemoveAll(data))
	}
	if err := errors.Join(errs...); err != nil {
		winutil.MessageBox(title, "Uninstalled, but not everything could be removed: "+err.Error())
	} else {
		winutil.Dialog(title, "downloadmanagersquared is uninstalled. Remove the extension in Firefox under Add-ons too.",
			windows.MB_OK|windows.MB_ICONINFORMATION)
	}
	// Last, as this exe lives in the folder: it can only go once this process has exited.
	if err := l.RemoveLater(); err != nil {
		winutil.MessageBox(title, "Could not remove "+l.Dir+": "+err.Error())
	}
}

// quitEngine asks this user's running engine to quit and waits until it has
// exited, so its files can be replaced. False if none answered.
func quitEngine() bool {
	res, err := call("quit")
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

// call sends one request to the running engine and returns its result.
func call(method string) (json.RawMessage, error) {
	c, err := ipc.Dial(2 * time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	b, _ := json.Marshal(ipc.Request{ID: 1, Method: method})
	if _, err := c.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		var resp struct {
			ID     int64           `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		}
		if json.Unmarshal(line, &resp) != nil || resp.ID != 1 {
			continue // an event
		}
		if resp.Error != "" {
			return nil, errors.New(resp.Error)
		}
		return resp.Result, nil
	}
}

func fail(err error) {
	winutil.MessageBox(title, "Could not start: "+err.Error())
	os.Exit(1)
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
