// Package install puts the engine in its folder (Program Files unless the user
// picks another) and registers it for the current user: the native-messaging
// host for the browser, start at login, shortcuts and the entry in Windows'
// Installed apps. Only copying into a folder the user can't write to runs as
// administrator; everything else stays per-user.
package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"downloadmanagersquared/engine/internal/atomicfile"
	"downloadmanagersquared/engine/internal/winutil"
)

const (
	Name        = "downloadmanagersquared"
	ExtensionID = "downloadmanagersquared@dms"
	// ChromiumExtensionID is the ID Chromium derives from the key in
	// extension/dev/chromium.js: SHA-256 of the DER public key, first 32 hex
	// digits with 0-9a-f mapped to a-p.
	ChromiumExtensionID = "lolpolhdlianfdogddkkdoldkpehihdc"
)

// Repo is the GitHub owner/repo releases come from, set at build time
// (build.ps1 -Repo).
var Repo = "unicrncndms/downloadmanagersquared"

// ReleasesURL is the page listing the releases.
func ReleasesURL() string { return "https://github.com/" + Repo + "/releases/latest" }

// AMOListingURL is the extension's page on addons.mozilla.org, where Firefox
// and its forks add it from. The slug must match the one set on the AMO listing.
func AMOListingURL() string {
	return "https://addons.mozilla.org/firefox/addon/downloadmanagersquared/"
}

// ChromiumStoreURL is the extension's Chrome Web Store page, where Chrome,
// Edge, Brave and Chromium all add it from.
func ChromiumStoreURL() string {
	return "https://chromewebstore.google.com/detail/" + ChromiumExtensionID
}

// Layout is where an install lives: its folder, its shortcuts and its
// registry entries under HKEY_CURRENT_USER. Tests point it at temp folders
// and a test key.
type Layout struct {
	Dir          string
	StartMenuDir string // the user's Start menu programs folder
	DesktopDir   string
	MozillaKey   string // the native-messaging host for the Firefox family
	// ChromiumKeys are the native-messaging host keys of Chrome, Edge, Brave
	// and Chromium, each pointing at the Chromium host manifest.
	ChromiumKeys []string
	RunKey       string // holds the start-at-login value
	UninstallKey string // the Installed apps entry
	Icon         []byte // the app icon (ICO), written next to the exes for the shortcuts
	// Bundled holds the files the engine exe carries (dms-host.exe and the
	// notices), so it installs on its own, e.g. run straight from the zip.
	// They win over files next to the exe. Nil, or empty in a dev build:
	// the files next to the exe are used.
	Bundled fs.FS
}

// Options are what a first install sets up besides the engine.
type Options struct {
	StartMenu, Desktop, Autostart bool
}

// Default is a new install for the current user, in Program Files.
func Default() (Layout, error) {
	var dirs [3]string
	for i, id := range []*windows.KNOWNFOLDERID{windows.FOLDERID_ProgramFiles, windows.FOLDERID_Programs, windows.FOLDERID_Desktop} {
		d, err := windows.KnownFolderPath(id, 0)
		if err != nil {
			return Layout{}, err
		}
		dirs[i] = d
	}
	return Layout{
		Dir:          filepath.Join(dirs[0], Name),
		StartMenuDir: dirs[1],
		DesktopDir:   dirs[2],
		MozillaKey:   `Software\Mozilla\NativeMessagingHosts\` + Name,
		ChromiumKeys: []string{
			`Software\Google\Chrome\NativeMessagingHosts\` + Name,
			`Software\Microsoft\Edge\NativeMessagingHosts\` + Name,
			`Software\BraveSoftware\Brave-Browser\NativeMessagingHosts\` + Name,
			`Software\Chromium\NativeMessagingHosts\` + Name,
		},
		RunKey:       `Software\Microsoft\Windows\CurrentVersion\Run`,
		UninstallKey: `Software\Microsoft\Windows\CurrentVersion\Uninstall\` + Name,
	}, nil
}

// Find is l moved to the folder this user installed into, read from the
// Installed apps entry. False if there is no install (l is then unchanged).
func (l Layout) Find() (Layout, bool) {
	dir := winutil.RegString(registry.CURRENT_USER, l.UninstallKey, "InstallLocation")
	if dir == "" {
		return l, false
	}
	if _, err := os.Stat(filepath.Join(dir, "dms-engine.exe")); err != nil {
		return l, false
	}
	l.Dir = dir
	return l, true
}

func (l Layout) Engine() string           { return filepath.Join(l.Dir, "dms-engine.exe") }
func (l Layout) Host() string             { return filepath.Join(l.Dir, "dms-host.exe") }
func (l Layout) StartMenuLink() string    { return filepath.Join(l.StartMenuDir, Name+".lnk") }
func (l Layout) DesktopLink() string      { return filepath.Join(l.DesktopDir, Name+".lnk") }
func (l Layout) manifest() string         { return filepath.Join(l.Dir, "native-host.json") }
func (l Layout) chromiumManifest() string { return filepath.Join(l.Dir, "native-host-chromium.json") }
func (l Layout) iconFile() string         { return filepath.Join(l.Dir, "dms.ico") }

// Notices are the licence files the release zip carries next to the exes. The
// install folder gets them too, so the installed copy keeps its notices.
var Notices = []string{"LICENSE", "THIRD-PARTY-NOTICES"}

// files are everything an install puts in its folder. Uninstalling removes
// only these, as the folder may be one the user keeps other things in.
func (l Layout) files() []string {
	out := []string{l.Engine(), l.Host(), l.manifest(), l.chromiumManifest(), l.iconFile()}
	for _, f := range Notices {
		out = append(out, filepath.Join(l.Dir, f))
	}
	return out
}

// Contains is whether exe sits in the install folder.
func (l Layout) Contains(exe string) bool {
	return strings.EqualFold(filepath.Clean(filepath.Dir(exe)), filepath.Clean(l.Dir))
}

// Install copies both exes from src into the install folder and registers
// them. first is what a first install sets up; nil on an upgrade, which
// leaves start at login and the shortcuts as the user has them.
func (l Layout) Install(src, version string, first *Options) error {
	if _, err := os.Stat(filepath.Join(src, "dms-engine.exe")); err != nil {
		return fmt.Errorf("dms-engine.exe is missing in %s", src)
	}
	if !l.has(src, "dms-host.exe") {
		return errors.New("dms-host.exe is missing: this build doesn't carry it and it isn't next to dms-engine.exe")
	}
	if winutil.CanWrite(l.Dir) {
		if err := l.PutFiles(src); err != nil {
			return err
		}
	} else if err := l.putFilesElevated(src); err != nil {
		return err
	}
	winutil.IconsChanged(l.Engine(), l.iconFile(), l.StartMenuLink(), l.DesktopLink())
	if err := l.Register(); err != nil {
		return fmt.Errorf("the files are in %s but DMS² could not be registered with the browser: %w", l.Dir, err)
	}
	if first != nil {
		if err := l.setUp(*first); err != nil {
			return err
		}
	}
	return l.writeUninstallEntry(version)
}

// PutFiles writes the install folder: the engine from src, the host and the
// licence files from what the engine carries (else from src), the native host
// manifest and the icon. It touches no registry, so it can run as an
// administrator who isn't the user installing.
func (l Layout) PutFiles(src string) error {
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return err
	}
	inPlace := strings.EqualFold(filepath.Clean(src), filepath.Clean(l.Dir)) // installing where it was extracted
	// What an earlier update moved aside, now that nothing runs it.
	if old, _ := filepath.Glob(filepath.Join(l.Dir, "*.old-*")); len(old) > 0 {
		for _, f := range old {
			os.Remove(f)
		}
	}
	if !inPlace {
		if err := retrying(l.Engine(), func(dst string) error { return copyFile(filepath.Join(src, "dms-engine.exe"), dst) }); err != nil {
			return err
		}
	}
	for _, f := range append([]string{"dms-host.exe"}, Notices...) {
		if err := l.place(src, f, inPlace); err != nil && !(errors.Is(err, fs.ErrNotExist) && f != "dms-host.exe") {
			return err // a dev build run from dist\ has no notices
		}
	}
	if len(l.Icon) > 0 {
		if err := atomicfile.Write(l.iconFile(), l.Icon, 0o644); err != nil {
			return err
		}
	}
	return l.writeManifests()
}

// FlagPutFiles makes dms-engine.exe run PutFiles from its own folder into the
// folder that follows the flag. Install starts it so, as administrator.
const FlagPutFiles = "--put-files"

func (l Layout) putFilesElevated(src string) error {
	exe := filepath.Join(src, "dms-engine.exe")
	// The folder src is the user's to write: hold the exe unchanged until the
	// elevated copy has started from it (see ADR 0008).
	hold, err := winutil.HoldUnchanged(exe)
	if err != nil {
		return err
	}
	defer hold.Close()
	code, err := winutil.RunElevated(exe, FlagPutFiles+" "+syscall.EscapeArg(l.Dir), true)
	if errors.Is(err, winutil.ErrElevationDeclined) {
		return fmt.Errorf("installing into %s needs administrator rights. Allow it when Windows asks, or choose a folder of your own", l.Dir)
	}
	if err != nil {
		return err
	}
	if code != 0 {
		return winutil.ErrShown // the elevated copy said what went wrong
	}
	return nil
}

// setUp does what the user chose on a first install.
func (l Layout) setUp(o Options) error {
	if o.Autostart {
		if err := l.setRun(); err != nil {
			return err
		}
	}
	icon := ""
	if _, err := os.Stat(l.iconFile()); err == nil {
		icon = l.iconFile()
	}
	for _, s := range []struct {
		on   bool
		path string
	}{{o.StartMenu, l.StartMenuLink()}, {o.Desktop, l.DesktopLink()}} {
		if !s.on {
			continue
		}
		if err := winutil.CreateShortcut(s.path, l.Engine(), l.Dir, "Downloads over all your connections at once", icon); err != nil {
			return fmt.Errorf("creating the shortcut %s: %w", s.path, err)
		}
	}
	return nil
}

// has is whether name is carried by the engine or lies in src.
func (l Layout) has(src, name string) bool {
	if l.Bundled != nil {
		if _, err := fs.Stat(l.Bundled, name); err == nil {
			return true
		}
	}
	_, err := os.Stat(filepath.Join(src, name))
	return err == nil
}

// place writes name into the install folder from what the engine carries,
// else from src. In place (src is the install folder) a file already there stays.
func (l Layout) place(src, name string, inPlace bool) error {
	dst := filepath.Join(l.Dir, name)
	if l.Bundled != nil {
		if b, err := fs.ReadFile(l.Bundled, name); err == nil {
			return retrying(dst, func(p string) error { return os.WriteFile(p, b, 0o755) })
		}
	}
	if inPlace {
		_, err := os.Stat(dst)
		return err
	}
	return retrying(dst, func(p string) error { return copyFile(filepath.Join(src, name), p) })
}

// retrying writes dst through write (which writes the path it is given),
// retrying for a few seconds. A file that isn't there fails at once.
func retrying(dst string, write func(string) error) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := replace(dst, write)
		if err == nil || errors.Is(err, fs.ErrNotExist) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// replace writes dst, or, while a running program holds it (an engine or a
// host that a browser's older dms-host started again during the update: no
// guard holds those back), writes a new file beside it and swaps it in after
// moving the running one aside. Windows lets a running exe be renamed, not
// overwritten; the next start runs the new one, and the next install removes
// the one set aside.
func replace(dst string, write func(string) error) error {
	err := write(dst)
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return err
	}
	tmp := dst + ".new"
	if err := write(tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	aside := fmt.Sprintf("%s.old-%d", dst, time.Now().UnixNano())
	if err := os.Rename(dst, aside); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Rename(aside, dst)
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Register makes every supported browser find the native host, rewriting what
// is missing or points elsewhere: the one HKCU Mozilla key that Firefox,
// LibreWolf, Waterfox and Floorp all read points at the Firefox host manifest,
// and the Chrome, Edge, Brave and Chromium keys at the Chromium one (the two
// browsers reject each other's manifest keys). It rewrites the manifests too
// if missing or pointing elsewhere, which in a folder only an administrator
// can write to only the install does.
func (l Layout) Register() error {
	err := l.writeManifests()
	if err != nil && !errors.Is(err, fs.ErrPermission) {
		return err
	}
	errs := []error{err, setValue(l.MozillaKey, "", l.manifest())}
	for _, key := range l.ChromiumKeys {
		errs = append(errs, setValue(key, "", l.chromiumManifest()))
	}
	return errors.Join(errs...)
}

func (l Layout) writeManifests() error {
	return errors.Join(
		l.writeManifest(l.manifest(), "allowed_extensions", ExtensionID),
		l.writeManifest(l.chromiumManifest(), "allowed_origins", "chrome-extension://"+ChromiumExtensionID+"/"))
}

func (l Layout) writeManifest(file, allowKey, allowed string) error {
	want, _ := json.MarshalIndent(map[string]any{
		"name":        Name,
		"description": "downloadmanagersquared engine",
		"path":        l.Host(),
		"type":        "stdio",
		allowKey:      []string{allowed},
	}, "", "  ")
	if have, err := os.ReadFile(file); err == nil && string(have) == string(want) {
		return nil
	}
	return atomicfile.Write(file, want, 0o644)
}

func (l Layout) setRun() error {
	return setValue(l.RunKey, Name, `"`+l.Engine()+`"`)
}

func (l Layout) writeUninstallEntry(version string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, l.UninstallKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	for name, v := range map[string]string{
		"DisplayName":     "DMS²",
		"DisplayVersion":  version,
		"Publisher":       "downloadmanagersquared",
		"DisplayIcon":     l.Engine(),
		"InstallLocation": l.Dir,
		"UninstallString": `"` + l.Engine() + `" --uninstall`,
		"URLInfoAbout":    "https://github.com/" + Repo,
	} {
		if err := k.SetStringValue(name, v); err != nil {
			return err
		}
	}
	for _, name := range []string{"NoModify", "NoRepair"} {
		if err := k.SetDWordValue(name, 1); err != nil {
			return err
		}
	}
	return nil
}

// Unregister removes start at login, the shortcuts, the browser registration
// and the Installed apps entry.
func (l Layout) Unregister() error {
	var errs []error
	if k, err := registry.OpenKey(registry.CURRENT_USER, l.RunKey, registry.SET_VALUE); err == nil {
		if err := k.DeleteValue(Name); err != nil && !errors.Is(err, registry.ErrNotExist) {
			errs = append(errs, err)
		}
		k.Close()
	}
	for _, lnk := range []string{l.StartMenuLink(), l.DesktopLink()} {
		if err := os.Remove(lnk); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	for _, key := range append([]string{l.MozillaKey, l.UninstallKey}, l.ChromiumKeys...) {
		if err := registry.DeleteKey(registry.CURRENT_USER, key); err != nil && !errors.Is(err, registry.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Restore puts back what Unregister removed that the extension and Windows
// need (the browser registration and the Installed apps entry), when the files
// could not be removed after all (the administrator prompt was declined).
func (l Layout) Restore(version string) error {
	return errors.Join(l.Register(), l.writeUninstallEntry(version))
}

// RemoveLater deletes the install's files once this process (which runs from
// the folder) has exited, then the folder if nothing else is left in it: a
// cmd keeps trying while a file is still in use (see removeCommand). It runs
// as administrator if the folder needs it, so Windows asks now, not once the
// process has exited.
func (l Layout) RemoveLater() error {
	cmdExe := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	if !winutil.CanWrite(l.Dir) {
		// The folder comes from a registry value any process of the user can set:
		// never elevate a command for one that isn't an install folder, or that
		// cmd would read differently ("%" expands, "\"" ends the quote).
		if !strings.EqualFold(filepath.Base(l.Dir), Name) || strings.ContainsAny(l.Dir, "%\"^&") {
			return fmt.Errorf("%s is not a DMS² install folder; delete it yourself if it is", l.Dir)
		}
		_, err := winutil.RunElevated(cmdExe, l.removeCommand(), false)
		return err
	}
	cmd := exec.Command(cmdExe)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       `cmd.exe ` + l.removeCommand(),
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.DETACHED_PROCESS,
	}
	cmd.Dir = os.TempDir() // not the folder it deletes
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// removeCommand is cmd's arguments for RemoveLater. It deletes the install's
// own files by name, and the folder without /s, so it never takes anything
// the user keeps there. It tries each second while an exe is still there:
// the uninstall window that started it may stay open a while (up to ten
// minutes), and a dms-host may still be exiting. Once both are gone it no
// longer waits.
func (l Layout) removeCommand() string {
	try := ""
	for _, exe := range []string{l.Engine(), l.Host()} {
		try += `(if exist "` + exe + `" ping -n 2 127.0.0.1 >nul) & `
	}
	try += `del /f /q`
	for _, f := range l.files() {
		try += ` "` + f + `"`
	}
	try += ` 2>nul & rmdir "` + l.Dir + `" 2>nul`
	return `/d /c for /l %i in (1,1,600) do @(` + try + `)`
}

func setValue(key, name, v string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, key, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if have, _, err := k.GetStringValue(name); err == nil && have == v {
		return nil
	}
	return k.SetStringValue(name, v)
}

// deleteTree removes a key and everything below it.
func deleteTree(root registry.Key, path string) error {
	k, err := registry.OpenKey(root, path, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return err
	}
	subs, _ := k.ReadSubKeyNames(-1)
	k.Close()
	for _, s := range subs {
		deleteTree(root, path+`\`+s)
	}
	return registry.DeleteKey(root, path)
}
