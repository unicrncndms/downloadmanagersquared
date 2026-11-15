// dms-engine is the background download engine with its tray icon. It also
// installs itself: started from anywhere but the install folder, it asks
// where to install (or upgrades the install there), copies itself there and
// starts that copy instead.
// Build with -ldflags "-H windowsgui" so no console window appears.
package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"

	"downloadmanagersquared/engine/internal/bundle"
	"downloadmanagersquared/engine/internal/engine"
	"downloadmanagersquared/engine/internal/install"
	"downloadmanagersquared/engine/internal/ipc"
	"downloadmanagersquared/engine/internal/setup"
	"downloadmanagersquared/engine/internal/tray"
	"downloadmanagersquared/engine/internal/ui"
	"downloadmanagersquared/engine/internal/window"
	"downloadmanagersquared/engine/internal/winutil"
)

const title = "DMS²"

// Flags the installing copy passes to the installed one.
const (
	flagUpdated   = "--updated"   // upgrade: say so in a balloon
	flagInstalled = "--installed" // first install: a balloon points at the tray icon
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
	l.Icon, l.Bundled = tray.Icon(), bundle.Files
	l, installed := l.Find()
	if slices.Contains(os.Args[1:], flagUninstall) {
		uninstall(l)
		return
	}
	if !l.Contains(self) {
		src := filepath.Dir(self)
		if installed { // an upgrade goes where the install is, without asking
			if err := installFrom(l, src, nil); err != nil {
				if !errors.Is(err, winutil.ErrShown) {
					winutil.MessageBox(title, "Could not install: "+err.Error())
				}
				os.Exit(1)
			}
			return
		}
		setup.Run(setup.Choice{Dir: l.Dir, StartMenu: true, Autostart: true}, tray.Icon(), setup.Steps{
			Install: func(c setup.Choice) error {
				l.Dir = c.Dir
				return installFrom(l, src, &install.Options{StartMenu: c.StartMenu, Desktop: c.Desktop, Autostart: c.Autostart})
			},
			Browsers: winutil.FirefoxBrowsers,
			Version:  engine.Version,
			Connected: func() []string {
				res, err := ipc.Call("extensionBrowsers")
				var exes []string
				if err == nil {
					json.Unmarshal(res, &exes)
				}
				return exes
			},
			OpenFile: func() {
				if err := winutil.ShellOpen(extensionTarget(src)); err != nil {
					winutil.MessageBox(title, "Could not open the extension file: "+err.Error())
				}
			},
			AddExtension: func(b winutil.Browser) {
				if err := winutil.OpenBrowser(b.Exe, extensionTarget(src)); err != nil {
					winutil.MessageBox(title, "Could not open "+b.Name+": "+err.Error())
				}
			},
			GetFirefox: func() { winutil.ShellOpen("https://www.mozilla.org/firefox/new/") },
			Uninstall: func() {
				if err := winutil.StartDetached(l.Engine(), flagUninstall); err != nil {
					winutil.MessageBox(title, "Could not start uninstalling: "+err.Error())
				}
			},
		})
		return
	}
	run(l)
}

// extensionTarget is the signed extension the release zip carries: the browser
// asks once to add it. A build without one gets it from the release.
func extensionTarget(src string) string {
	target := filepath.Join(src, "downloadmanagersquared.xpi")
	if _, err := os.Stat(target); err != nil {
		return install.ExtensionURL()
	}
	return target
}

// run is the installed engine.
func run(l install.Layout) {
	e, err := engine.New()
	if errors.Is(err, engine.ErrAlreadyRunning) {
		// Launched twice (e.g. from the Start menu): the running engine shows
		// its Downloads window instead. Windows lets a process take the
		// foreground only if the one in front allows it, as this one may.
		winutil.AllowForeground()
		ipc.Call("openManager")
		return
	}
	if err != nil {
		fail(err)
	}
	setupLog(e.Dir())
	stopProfile := startProfile(e.Dir())
	log.Printf("engine %s starting", engine.Version)
	if err := l.Register(); err != nil { // a browser can't start us without it
		log.Printf("registering the native host: %v", err)
	}
	if slices.Contains(os.Args[1:], flagUpdated) {
		winutil.Notify("Updated", "DMS² is updated to v"+engine.Version+".")
	}
	if slices.Contains(os.Args[1:], flagInstalled) {
		// Windows 11 hides a new tray icon in the overflow: say where it is.
		winutil.Notify("DMS² is running", "Its icon is in the tray, next to the clock. If you cannot see it, click the arrow to find it.")
	}
	w := downloadsWindow(e)
	e.SetWindow(w)
	go e.Run()
	tray.Run(e, w)
	stopProfile()
	log.Printf("engine stopped")
}

// downloadsWindow is the engine's own window, showing the pages it carries.
func downloadsWindow(e *engine.Engine) *window.Window {
	cfg := window.Config{DataDir: e.Dir(), Dial: e.Dial, Icon: tray.Icon()}
	if ui.Present() {
		cfg.Pages = ui.Pages
	}
	return window.New(cfg)
}

func fail(err error) {
	winutil.MessageBox(title, "Could not start: "+err.Error())
	os.Exit(1)
}
