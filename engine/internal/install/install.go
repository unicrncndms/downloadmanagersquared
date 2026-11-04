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
)

// Repo is the GitHub owner/repo releases come from, set at build time
// (build.ps1 -Repo).
var Repo = "unicrncndms/downloadmanagersquared"

// ReleasesURL is the page listing the releases.
func ReleasesURL() string { return "https://github.com/" + Repo + "/releases/latest" }

// ExtensionURL is the latest signed extension.
func ExtensionURL() string {
	return "https://github.com/" + Repo + "/releases/latest/download/downloadmanagersquared.xpi"
}

// Layout is where an install lives: its folder, its shortcuts and its
// registry entries under HKEY_CURRENT_USER. Tests point it at temp folders
// and a test key.
type Layout struct {
	Dir          string
	StartMenuDir string // the user's Start menu programs folder
	DesktopDir   string
	MozillaKey   string // the native-messaging host for the Firefox family
	RunKey       string // holds the start-at-login value
	UninstallKey string // the Installed apps entry
	Icon         []byte // the app icon (ICO), written next to the exes for the shortcuts
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
		RunKey:       `Software\Microsoft\Windows\CurrentVersion\Run`,
		UninstallKey: `Software\Microsoft\Windows\CurrentVersion\Uninstall\` + Name,
	}, nil
}

// Find is l moved to the folder this user installed into, read from the
// Installed apps entry. False if there is no install (l is then unchanged).
func (l Layout) Find() (Layout, bool) {
	dir := readValue(l.UninstallKey, "InstallLocation")
	if dir == "" {
		return l, false
	}
	if _, err := os.Stat(filepath.Join(dir, "dms-engine.exe")); err != nil {
		return l, false
	}
	l.Dir = dir
	return l, true
}

func (l Layout) Engine() string        { return filepath.Join(l.Dir, "dms-engine.exe") }
func (l Layout) Host() string          { return filepath.Join(l.Dir, "dms-host.exe") }
func (l Layout) StartMenuLink() string { return filepath.Join(l.StartMenuDir, Name+".lnk") }
func (l Layout) DesktopLink() string   { return filepath.Join(l.DesktopDir, Name+".lnk") }
func (l Layout) manifest() string      { return filepath.Join(l.Dir, "native-host.json") }
func (l Layout) iconFile() string      { return filepath.Join(l.Dir, "dms.ico") }

// files are everything an install puts in its folder. Uninstalling removes
// only these, as the folder may be one the user keeps other things in.
func (l Layout) files() []string {
	return []string{l.Engine(), l.Host(), l.manifest(), l.iconFile()}
}

// Contains is whether exe sits in the install folder.
func (l Layout) Contains(exe string) bool {
	return strings.EqualFold(filepath.Clean(filepath.Dir(exe)), filepath.Clean(l.Dir))
}

// ErrShown is a failure the user was already told about.
var ErrShown = errors.New("already reported")

// Install copies both exes from src into the install folder and registers
// them. first is what a first install sets up; nil on an upgrade, which
// leaves start at login and the shortcuts as the user has them.
func (l Layout) Install(src, version string, first *Options) error {
	for _, f := range []string{"dms-engine.exe", "dms-host.exe"} {
		if _, err := os.Stat(filepath.Join(src, f)); err != nil {
			return fmt.Errorf("%s is missing next to this program: extract the whole zip first, then run dms-engine.exe from there", f)
		}
	}
	if winutil.CanWrite(l.Dir) {
		if err := l.PutFiles(src); err != nil {
			return err
		}
	} else if err := l.putFilesElevated(src); err != nil {
		return err
	}
	if err := l.Register(); err != nil {
		return err
	}
	if first != nil {
		if err := l.setUp(*first); err != nil {
			return err
		}
	}
	return l.writeUninstallEntry(version)
}

// PutFiles writes the install folder: both exes from src, the native host
// manifest and the icon. It touches no registry, so it can run as an
// administrator who isn't the user installing.
func (l Layout) PutFiles(src string) error {
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(src), filepath.Clean(l.Dir)) { // else installing where it was extracted
		for _, f := range []string{"dms-engine.exe", "dms-host.exe"} {
			if err := copyRetrying(filepath.Join(src, f), filepath.Join(l.Dir, f), 10*time.Second); err != nil {
				return err
			}
		}
	}
	if len(l.Icon) > 0 {
		if err := atomicfile.Write(l.iconFile(), l.Icon, 0o644); err != nil {
			return err
		}
	}
	return l.writeManifest()
}

// FlagPutFiles makes dms-engine.exe run PutFiles from its own folder into the
// folder that follows the flag. Install starts it so, as administrator.
const FlagPutFiles = "--put-files"

func (l Layout) putFilesElevated(src string) error {
	code, err := winutil.RunElevated(filepath.Join(src, "dms-engine.exe"), FlagPutFiles+` "`+l.Dir+`"`, true)
	if errors.Is(err, winutil.ErrElevationDeclined) {
		return fmt.Errorf("installing into %s needs administrator rights. Allow it when Windows asks, or choose a folder of your own", l.Dir)
	}
	if err != nil {
		return err
	}
	if code != 0 {
		return ErrShown // the elevated copy said what went wrong
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

// copyRetrying copies a file, retrying while the target is still in use (a
// dms-host that hasn't exited yet).
func copyRetrying(src, dst string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		err := copyFile(src, dst)
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
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

// Register makes every supported browser family find the native host,
// rewriting what is missing or points elsewhere. A Chromium family would get a
// function of its own next to registerFirefox.
func (l Layout) Register() error {
	return l.registerFirefox()
}

// registerFirefox points the one HKCU Mozilla key that Firefox, LibreWolf,
// Waterfox and Floorp all read at the host manifest. It rewrites the manifest
// too if that is missing or points elsewhere, which in a folder only an
// administrator can write to only the install does.
func (l Layout) registerFirefox() error {
	err := l.writeManifest()
	if err != nil && !errors.Is(err, fs.ErrPermission) {
		return err
	}
	return errors.Join(err, setValue(l.MozillaKey, "", l.manifest()))
}

func (l Layout) writeManifest() error {
	want, _ := json.MarshalIndent(map[string]any{
		"name":               Name,
		"description":        "downloadmanagersquared engine",
		"path":               l.Host(),
		"type":               "stdio",
		"allowed_extensions": []string{ExtensionID},
	}, "", "  ")
	if have, err := os.ReadFile(l.manifest()); err == nil && string(have) == string(want) {
		return nil
	}
	return atomicfile.Write(l.manifest(), want, 0o644)
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
		"DisplayName":     "downloadmanagersquared (DMS²)",
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
	for _, key := range []string{l.MozillaKey, l.UninstallKey} {
		if err := registry.DeleteKey(registry.CURRENT_USER, key); err != nil && !errors.Is(err, registry.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// RemoveLater deletes the install's files once this process (which runs from
// the folder) has exited, then the folder if nothing else is left in it: a
// cmd waits a few seconds and tries a few times while a file is still in use
// (a dms-host still exiting). It runs as administrator if the folder needs it.
func (l Layout) RemoveLater() error {
	cmdExe := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	if !winutil.CanWrite(l.Dir) {
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
// the user keeps there.
func (l Layout) removeCommand() string {
	try := `ping -n 3 127.0.0.1 >nul & del /f /q`
	for _, f := range l.files() {
		try += ` "` + f + `"`
	}
	try += ` 2>nul & rmdir "` + l.Dir + `" 2>nul`
	return `/d /c ` + try + ` & ` + try + ` & ` + try + ` & ` + try
}

func readValue(key, name string) string {
	k, err := registry.OpenKey(registry.CURRENT_USER, key, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, _ := k.GetStringValue(name)
	return v
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
