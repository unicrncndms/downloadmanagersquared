// dms-engine is the background download engine with its tray icon. It also
// installs itself: started from anywhere but the install folder, it copies
// itself there (or upgrades the copy there) and starts that one instead.
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
	"downloadmanagersquared/engine/internal/tray"
	"downloadmanagersquared/engine/internal/winutil"
)

const title = "downloadmanagersquared"

// Flags the installing copy passes to the installed one.
const (
	flagInstalled = "--installed" // first install: show how to add the extension
	flagUpdated   = "--updated"   // upgrade: say so in a balloon
	flagUninstall = "--uninstall" // from Installed apps
)

func main() {
	l, err := install.Default()
	if err != nil {
		fail(err)
	}
	self, err := os.Executable()
	if err != nil {
		fail(err)
	}
	if slices.Contains(os.Args[1:], flagUninstall) {
		uninstall(l)
		return
	}
	if !l.Contains(self) {
		if err := installFrom(l, filepath.Dir(self)); err != nil {
			winutil.MessageBox(title, "Could not install: "+err.Error())
			os.Exit(1)
		}
		return
	}
	run(l)
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
	switch {
	case slices.Contains(os.Args[1:], flagInstalled):
		go installedBox()
	case slices.Contains(os.Args[1:], flagUpdated):
		winutil.Notify("Updated", "DMS² is updated to v"+engine.Version+".")
	}
	go e.Run()
	tray.Run(e)
	log.Printf("engine stopped")
}

// installedBox is the one thing a first install shows.
func installedBox() {
	url := install.ExtensionURL()
	r := winutil.Dialog(title, "DMS² is installed and running in the tray, next to the clock.\n\n"+
		"One step left: add the Firefox extension. OK opens it in Firefox:\n\n"+url+"\n\n"+
		"Using LibreWolf, Waterfox or Floorp? Open the link there instead.",
		windows.MB_OKCANCEL|windows.MB_ICONINFORMATION)
	if r == winutil.IDOK {
		if err := winutil.OpenInFirefox(url); err != nil {
			log.Printf("opening %s: %v", url, err)
		}
	}
}

// installFrom installs or upgrades from the folder this exe was started in,
// then starts the installed engine.
func installFrom(l install.Layout, src string) error {
	upgrade := false
	if quitEngine() {
		upgrade = true
	} else if len(winutil.ProcessesOf(l.Engine())) > 0 {
		// An engine from before the quit method and the per-user pipe.
		winutil.Dialog(title, "An older DMS² engine is running. Quit it from its tray icon (right-click → Quit), then press OK to update.",
			windows.MB_OK|windows.MB_ICONINFORMATION)
		for _, pid := range winutil.ProcessesOf(l.Engine()) {
			winutil.WaitExit(pid, 30*time.Second)
		}
		upgrade = true
	} else if _, err := os.Stat(l.Engine()); err == nil {
		upgrade = true // installed, but not running
	}
	if err := l.Install(src, engine.Version, !upgrade); err != nil {
		return err
	}
	flag := flagInstalled
	if upgrade {
		flag = flagUpdated
	}
	return winutil.StartDetached(l.Engine(), flag)
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
	errs = append(errs, l.RemoveLater())
	if err := errors.Join(errs...); err != nil {
		winutil.MessageBox(title, "Uninstalled, but not everything could be removed: "+err.Error())
		return
	}
	winutil.Dialog(title, "downloadmanagersquared is uninstalled. Remove the extension in Firefox under Add-ons too.",
		windows.MB_OK|windows.MB_ICONINFORMATION)
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
