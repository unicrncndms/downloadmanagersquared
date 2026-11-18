package install

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// testLayout installs into a temp folder and registers under a test key of its own.
func testLayout(t *testing.T) Layout {
	t.Helper()
	base := fmt.Sprintf(`Software\downloadmanagersquared-test-%d`, time.Now().UnixNano())
	t.Cleanup(func() { deleteTree(registry.CURRENT_USER, base) })
	return Layout{
		Dir:          filepath.Join(t.TempDir(), "downloadmanagersquared"),
		StartMenuDir: t.TempDir(),
		DesktopDir:   t.TempDir(),
		MozillaKey:   base + `\Mozilla\NativeMessagingHosts\downloadmanagersquared`,
		ChromiumKeys: []string{
			base + `\Chrome\NativeMessagingHosts\downloadmanagersquared`,
			base + `\Edge\NativeMessagingHosts\downloadmanagersquared`,
		},
		RunKey:       base + `\Run`,
		UninstallKey: base + `\Uninstall\downloadmanagersquared`,
	}
}

func release(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"dms-engine.exe", "dms-host.exe"} {
		os.WriteFile(filepath.Join(dir, f), []byte(content+" "+f), 0o755)
	}
	return dir
}

func value(t *testing.T, key, name string) string {
	t.Helper()
	k, err := registry.OpenKey(registry.CURRENT_USER, key, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, _ := k.GetStringValue(name)
	return v
}

func TestFirstInstallCopiesAndRegistersEverything(t *testing.T) {
	l := testLayout(t)
	if err := l.Install(release(t, "v1"), "0.3.0", &Options{Autostart: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(l.Host()); string(b) != "v1 dms-host.exe" {
		t.Fatalf("dms-host.exe holds %q", b)
	}
	manifest := value(t, l.MozillaKey, "")
	var m struct {
		Path    string   `json:"path"`
		Type    string   `json:"type"`
		Allowed []string `json:"allowed_extensions"`
	}
	b, _ := os.ReadFile(manifest)
	if err := json.Unmarshal(b, &m); err != nil || m.Path != l.Host() || m.Type != "stdio" || len(m.Allowed) != 1 || m.Allowed[0] != ExtensionID {
		t.Fatalf("native host manifest at %q: %s (%v)", manifest, b, err)
	}
	if got := value(t, l.RunKey, Name); got != `"`+l.Engine()+`"` {
		t.Fatalf("start at login %q", got)
	}
	if got := value(t, l.UninstallKey, "UninstallString"); got != `"`+l.Engine()+`" --uninstall` {
		t.Fatalf("UninstallString %q", got)
	}
	if got := value(t, l.UninstallKey, "DisplayVersion"); got != "0.3.0" {
		t.Fatalf("DisplayVersion %q", got)
	}
}

func TestInstallCopiesTheLicences(t *testing.T) {
	l := testLayout(t)
	src := release(t, "v1")
	for _, f := range Notices {
		os.WriteFile(filepath.Join(src, f), []byte("text of "+f), 0o644)
	}
	if err := l.Install(src, "0.3.0", nil); err != nil {
		t.Fatal(err)
	}
	for _, f := range Notices {
		if b, _ := os.ReadFile(filepath.Join(l.Dir, f)); string(b) != "text of "+f {
			t.Fatalf("%s holds %q", f, b)
		}
		if !strings.Contains(l.removeCommand(), filepath.Join(l.Dir, f)) {
			t.Fatalf("uninstalling leaves %s behind", f)
		}
	}
}

func TestInstallWithoutTheLicencesStillWorks(t *testing.T) {
	l := testLayout(t) // a dev build in dist\ has no LICENSE next to the exes
	if err := l.Install(release(t, "v1"), "0.3.0", nil); err != nil {
		t.Fatal(err)
	}
}

func TestAnUpgradeLeavesStartAtLoginAlone(t *testing.T) {
	l := testLayout(t)
	if err := l.Install(release(t, "v1"), "0.3.0", &Options{Autostart: true}); err != nil {
		t.Fatal(err)
	}
	k, _ := registry.OpenKey(registry.CURRENT_USER, l.RunKey, registry.SET_VALUE)
	k.DeleteValue(Name) // switched off in the tray
	k.Close()
	if err := l.Install(release(t, "v2"), "0.4.0", nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(l.Engine()); string(b) != "v2 dms-engine.exe" {
		t.Fatalf("dms-engine.exe holds %q", b)
	}
	if got := value(t, l.RunKey, Name); got != "" {
		t.Fatalf("the upgrade turned start at login back on: %q", got)
	}
	if got := value(t, l.UninstallKey, "DisplayVersion"); got != "0.4.0" {
		t.Fatalf("DisplayVersion %q", got)
	}
}

func TestRegisterWritesAChromiumManifestForTheChromiumKeys(t *testing.T) {
	l := testLayout(t)
	if err := l.Install(release(t, "v1"), "0.3.0", &Options{}); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(l.Dir, "native-host-chromium.json")
	for _, k := range l.ChromiumKeys {
		if got := value(t, k, ""); got != want {
			t.Errorf("%s points at %q", k, got)
		}
	}
	b, _ := os.ReadFile(want)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	origins, _ := m["allowed_origins"].([]any)
	if m["name"] != Name || m["path"] != l.Host() || m["type"] != "stdio" || len(origins) != 1 || origins[0] != "chrome-extension://"+ChromiumExtensionID+"/" || m["allowed_extensions"] != nil {
		t.Fatalf("Chromium manifest: %s", b)
	}
	b, _ = os.ReadFile(l.manifest())
	if strings.Contains(string(b), "allowed_origins") || !strings.Contains(string(b), `"allowed_extensions"`) {
		t.Fatalf("Firefox manifest: %s", b)
	}
	if err := l.Unregister(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(l.files(), "|"); !strings.Contains(got, want) || !strings.Contains(got, l.manifest()) {
		t.Errorf("an uninstall removes only %s", got)
	}
}

func TestRegisterRestoresAMissingOrMovedRegistration(t *testing.T) {
	l := testLayout(t)
	if err := l.Install(release(t, "v1"), "0.3.0", &Options{Autostart: true}); err != nil {
		t.Fatal(err)
	}
	deleteTree(registry.CURRENT_USER, l.MozillaKey)
	os.Remove(filepath.Join(l.Dir, "native-host.json"))
	if err := l.Register(); err != nil {
		t.Fatal(err)
	}
	if value(t, l.MozillaKey, "") == "" {
		t.Fatal("the Mozilla key didn't come back")
	}
	k, _, _ := registry.CreateKey(registry.CURRENT_USER, l.MozillaKey, registry.SET_VALUE)
	k.SetStringValue("", `C:\elsewhere\native-host.json`)
	k.Close()
	if err := l.Register(); err != nil {
		t.Fatal(err)
	}
	if got := value(t, l.MozillaKey, ""); got != filepath.Join(l.Dir, "native-host.json") {
		t.Fatalf("the Mozilla key points at %q", got)
	}
}

func TestUnregisterRemovesEveryEntry(t *testing.T) {
	l := testLayout(t)
	if err := l.Install(release(t, "v1"), "0.3.0", &Options{Autostart: true}); err != nil {
		t.Fatal(err)
	}
	if err := l.Unregister(); err != nil {
		t.Fatal(err)
	}
	for _, k := range append([]string{l.MozillaKey, l.UninstallKey}, l.ChromiumKeys...) {
		if key, err := registry.OpenKey(registry.CURRENT_USER, k, registry.QUERY_VALUE); err == nil {
			key.Close()
			t.Errorf("%s is still there", k)
		}
	}
	if value(t, l.RunKey, Name) != "" {
		t.Error("start at login is still on")
	}
}

func TestUnregisterRemovesTheShortcuts(t *testing.T) {
	l := testLayout(t)
	if err := l.Install(release(t, "v1"), "0.3.0", &Options{StartMenu: true, Desktop: true}); err != nil {
		t.Fatal(err)
	}
	if err := l.Unregister(); err != nil {
		t.Fatal(err)
	}
	for _, lnk := range []string{l.StartMenuLink(), l.DesktopLink()} {
		if _, err := os.Stat(lnk); err == nil {
			t.Errorf("%s is still there", lnk)
		}
	}
}

func TestFirstInstallSetsUpWhatWasChosen(t *testing.T) {
	l := testLayout(t)
	l.Icon = []byte("icon")
	if err := l.Install(release(t, "v1"), "0.3.0", &Options{StartMenu: true}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(l.StartMenuLink()); err != nil || fi.Size() == 0 {
		t.Errorf("no Start menu shortcut: %v", err)
	}
	if _, err := os.Stat(l.DesktopLink()); err == nil {
		t.Error("a desktop shortcut nobody asked for")
	}
	if got := value(t, l.RunKey, Name); got != "" {
		t.Errorf("start at login is on: %q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(l.Dir, "dms.ico")); string(b) != "icon" {
		t.Errorf("the icon file holds %q", b)
	}
}

func TestFindUsesTheFolderOfTheInstalledAppsEntry(t *testing.T) {
	l := testLayout(t)
	elsewhere := l
	elsewhere.Dir = filepath.Join(t.TempDir(), "picked")
	if _, ok := l.Find(); ok {
		t.Fatal("found an install before installing")
	}
	if err := elsewhere.Install(release(t, "v1"), "0.3.0", &Options{}); err != nil {
		t.Fatal(err)
	}
	got, ok := l.Find()
	if !ok || got.Dir != elsewhere.Dir {
		t.Fatalf("Find gave %q (%v), want %q", got.Dir, ok, elsewhere.Dir)
	}
	os.Remove(elsewhere.Engine())
	if _, ok := l.Find(); ok {
		t.Error("an entry whose engine is gone still counts as installed")
	}
}

func TestInstallingIntoTheFolderItWasExtractedTo(t *testing.T) {
	l := testLayout(t)
	src := release(t, "v1")
	l.Dir = src
	if err := l.Install(src, "0.3.0", &Options{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(l.Engine()); string(b) != "v1 dms-engine.exe" {
		t.Fatalf("dms-engine.exe holds %q", b)
	}
}

// The folder may be one the user keeps other things in (D:\Apps): removing
// the install must leave those alone, and the folder with them.
func TestRemoveLaterKeepsWhatTheUserPutInTheFolder(t *testing.T) {
	l := testLayout(t)
	l.Icon = []byte("icon")
	if err := l.Install(release(t, "v1"), "0.3.0", &Options{}); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(l.Dir, "notes.txt")
	os.WriteFile(mine, []byte("keep me"), 0o644)
	if strings.Contains(l.removeCommand(), "/s") {
		t.Fatalf("the command deletes recursively: %s", l.removeCommand())
	}
	run := func() {
		cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"))
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe ` + strings.ReplaceAll(l.removeCommand(), "ping -n 2", "ping -n 1")}
		cmd.Run() // its exit code is the last rmdir's, which fails while the folder isn't empty
	}
	run()
	for _, f := range l.files() {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s is still there", f)
		}
	}
	if b, _ := os.ReadFile(mine); string(b) != "keep me" {
		t.Fatalf("the user's file is gone or changed: %q", b)
	}
	os.Remove(mine)
	run()
	if _, err := os.Stat(l.Dir); err == nil {
		t.Error("the emptied folder is still there")
	}
}

func TestContainsTellsAnInstalledExeFromAnotherCopy(t *testing.T) {
	l := Layout{Dir: `C:\Users\x\AppData\Local\downloadmanagersquared`}
	if !l.Contains(`c:\users\X\appdata\local\downloadmanagersquared\dms-engine.exe`) {
		t.Error("the installed exe isn't recognised")
	}
	if l.Contains(`C:\Users\x\Downloads\downloadmanagersquared-windows\dms-engine.exe`) {
		t.Error("a copy in Downloads counts as installed")
	}
}

// Double-clicking dms-engine.exe inside the zip makes Explorer extract that
// one exe to a temp folder: the host and notices come from inside the exe.
func TestInstallFromALoneEngineWritesWhatItCarries(t *testing.T) {
	l := testLayout(t)
	l.Bundled = fstest.MapFS{
		"dms-host.exe":        {Data: []byte("bundled host")},
		"LICENSE":             {Data: []byte("licence")},
		"THIRD-PARTY-NOTICES": {Data: []byte("notices")},
	}
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "dms-engine.exe"), []byte("v1 engine"), 0o755)
	if err := l.Install(src, "0.3.0", &Options{}); err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]string{"dms-engine.exe": "v1 engine", "dms-host.exe": "bundled host", "LICENSE": "licence", "THIRD-PARTY-NOTICES": "notices"} {
		if b, _ := os.ReadFile(filepath.Join(l.Dir, f)); string(b) != want {
			t.Errorf("%s holds %q, want %q", f, b, want)
		}
	}
}

// What the exe carries wins over loose files next to it, which may be left
// over from another version.
func TestTheBundledHostWinsOverOneNextToTheExe(t *testing.T) {
	l := testLayout(t)
	l.Bundled = fstest.MapFS{"dms-host.exe": {Data: []byte("bundled host")}}
	if err := l.Install(release(t, "old"), "0.3.0", &Options{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(l.Host()); string(b) != "bundled host" {
		t.Fatalf("dms-host.exe holds %q", b)
	}
}

// A dev build carries nothing; without dms-host.exe next to it there is no install.
func TestInstallWithoutAnyHostSaysWhatIsMissing(t *testing.T) {
	l := testLayout(t)
	src := release(t, "v1")
	os.Remove(filepath.Join(src, "dms-host.exe"))
	err := l.Install(src, "0.3.0", &Options{Autostart: true})
	if err == nil || !strings.Contains(err.Error(), "dms-host.exe") {
		t.Fatalf("error %v, want one naming dms-host.exe", err)
	}
	if _, err := os.Stat(l.Dir); err == nil {
		t.Error("the install folder was made anyway")
	}
}

// Windows asks for administrator rights while the uninstall window is still
// open, so the removal starts before the engine has exited: it keeps trying
// until the engine's exe can go.
func TestRemoveLaterWaitsForTheEngineToExit(t *testing.T) {
	l := testLayout(t)
	l.Icon = []byte("icon")
	if err := l.Install(release(t, "v1"), "0.3.0", &Options{}); err != nil {
		t.Fatal(err)
	}
	inUse, err := os.Open(l.Engine()) // like the running exe: it can't be deleted
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"))
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe ` + l.removeCommand()}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Second) // longer than the few tries it used to make
	if _, err := os.Stat(l.Engine()); err != nil {
		t.Fatalf("the exe in use is gone already: %v", err)
	}
	inUse.Close()
	cmd.Wait()
	if _, err := os.Stat(l.Dir); err == nil {
		t.Fatal("the folder is still there once the exe was let go")
	}
}

// A running exe is open for reading and deleting only: it can't be written,
// but it can be renamed. An update writes the new one beside it and swaps it
// in; the next install removes the one set aside.
func TestAnUpdateReplacesAnExeThatIsRunning(t *testing.T) {
	src := t.TempDir()
	for _, f := range []string{"dms-engine.exe", "dms-host.exe"} {
		if err := os.WriteFile(filepath.Join(src, f), []byte("new "+f), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	l := testLayout(t)
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var held []windows.Handle
	for _, f := range []string{"dms-engine.exe", "dms-host.exe"} {
		p := filepath.Join(l.Dir, f)
		if err := os.WriteFile(p, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
		name, _ := windows.UTF16PtrFromString(p)
		h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, h)
	}
	if err := os.WriteFile(filepath.Join(l.Dir, "dms-engine.exe"), nil, 0o755); err == nil {
		t.Fatal("the held file could be written: the test doesn't hold it as a running exe is held")
	}
	if err := l.PutFiles(src); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"dms-engine.exe", "dms-host.exe"} {
		if b, _ := os.ReadFile(filepath.Join(l.Dir, f)); string(b) != "new "+f {
			t.Fatalf("%s is %q after the update", f, b)
		}
	}
	aside, _ := filepath.Glob(filepath.Join(l.Dir, "*.old-*"))
	if len(aside) != 2 {
		t.Fatalf("set aside: %v, want the two running exes", aside)
	}
	for _, h := range held {
		windows.CloseHandle(h)
	}
	if err := l.PutFiles(src); err != nil {
		t.Fatal(err)
	}
	if aside, _ := filepath.Glob(filepath.Join(l.Dir, "*.old-*")); len(aside) != 0 {
		t.Fatalf("still set aside after the next install: %v", aside)
	}
}
