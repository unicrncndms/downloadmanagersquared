// Package install puts the engine where it lives for the current user and
// registers it: the native-messaging host for the browser, start at login and
// the entry in Windows' Installed apps. No admin rights, no installer.
package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"downloadmanagersquared/engine/internal/atomicfile"
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

// Layout is where an install lives: its folder and its registry entries under
// HKEY_CURRENT_USER. Tests point it at a temp folder and a test key.
type Layout struct {
	Dir          string
	MozillaKey   string // the native-messaging host for the Firefox family
	RunKey       string // holds the start-at-login value
	UninstallKey string // the Installed apps entry
}

// Default is the current user's install.
func Default() (Layout, error) {
	local, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil {
		return Layout{}, err
	}
	return Layout{
		Dir:          filepath.Join(local, Name),
		MozillaKey:   `Software\Mozilla\NativeMessagingHosts\` + Name,
		RunKey:       `Software\Microsoft\Windows\CurrentVersion\Run`,
		UninstallKey: `Software\Microsoft\Windows\CurrentVersion\Uninstall\` + Name,
	}, nil
}

func (l Layout) Engine() string { return filepath.Join(l.Dir, "dms-engine.exe") }
func (l Layout) Host() string   { return filepath.Join(l.Dir, "dms-host.exe") }

// Contains is whether exe sits in the install folder.
func (l Layout) Contains(exe string) bool {
	return strings.EqualFold(filepath.Clean(filepath.Dir(exe)), filepath.Clean(l.Dir))
}

// Install copies both exes from src into the install folder and registers
// them. Only a first install switches start at login on: after that the
// tray's toggle owns it.
func (l Layout) Install(src, version string, first bool) error {
	for _, f := range []string{"dms-engine.exe", "dms-host.exe"} {
		if _, err := os.Stat(filepath.Join(src, f)); err != nil {
			return fmt.Errorf("%s is missing next to this program: extract the whole zip first, then run dms-engine.exe from there", f)
		}
	}
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return err
	}
	for _, f := range []string{"dms-engine.exe", "dms-host.exe"} {
		if err := copyRetrying(filepath.Join(src, f), filepath.Join(l.Dir, f), 10*time.Second); err != nil {
			return err
		}
	}
	if err := l.Register(); err != nil {
		return err
	}
	if first {
		if err := l.setRun(); err != nil {
			return err
		}
	}
	return l.writeUninstallEntry(version)
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

// registerFirefox writes the host manifest and the one HKCU Mozilla key that
// Firefox, LibreWolf, Waterfox and Floorp all read.
func (l Layout) registerFirefox() error {
	manifest := filepath.Join(l.Dir, "native-host.json")
	want, _ := json.MarshalIndent(map[string]any{
		"name":               Name,
		"description":        "downloadmanagersquared engine",
		"path":               l.Host(),
		"type":               "stdio",
		"allowed_extensions": []string{ExtensionID},
	}, "", "  ")
	if have, err := os.ReadFile(manifest); err != nil || string(have) != string(want) {
		if err := atomicfile.Write(manifest, want, 0o644); err != nil {
			return err
		}
	}
	return setValue(l.MozillaKey, "", manifest)
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

// Unregister removes start at login, the browser registration and the
// Installed apps entry.
func (l Layout) Unregister() error {
	var errs []error
	if k, err := registry.OpenKey(registry.CURRENT_USER, l.RunKey, registry.SET_VALUE); err == nil {
		if err := k.DeleteValue(Name); err != nil && !errors.Is(err, registry.ErrNotExist) {
			errs = append(errs, err)
		}
		k.Close()
	}
	for _, key := range []string{l.MozillaKey, l.UninstallKey} {
		if err := registry.DeleteKey(registry.CURRENT_USER, key); err != nil && !errors.Is(err, registry.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// RemoveLater deletes the install folder once this process (which runs from
// it) has exited: a detached cmd waits a few seconds, then removes it, trying
// again a few times while a file is still in use (a dms-host still exiting).
func (l Layout) RemoveLater() error {
	try := `ping -n 3 127.0.0.1 >nul & if exist "` + l.Dir + `" rmdir /s /q "` + l.Dir + `"`
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       `cmd.exe /d /c ` + try + ` & ` + try + ` & ` + try + ` & ` + try,
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.DETACHED_PROCESS,
	}
	cmd.Dir = os.TempDir() // not the folder it deletes
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
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
