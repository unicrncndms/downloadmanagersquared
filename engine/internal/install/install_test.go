package install

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

// testLayout installs into a temp folder and registers under a test key of its own.
func testLayout(t *testing.T) Layout {
	t.Helper()
	base := fmt.Sprintf(`Software\downloadmanagersquared-test-%d`, time.Now().UnixNano())
	t.Cleanup(func() { deleteTree(registry.CURRENT_USER, base) })
	return Layout{
		Dir:          filepath.Join(t.TempDir(), "downloadmanagersquared"),
		MozillaKey:   base + `\Mozilla\NativeMessagingHosts\downloadmanagersquared`,
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
	if err := l.Install(release(t, "v1"), "0.3.0", true); err != nil {
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

func TestAnUpgradeLeavesStartAtLoginAlone(t *testing.T) {
	l := testLayout(t)
	if err := l.Install(release(t, "v1"), "0.3.0", true); err != nil {
		t.Fatal(err)
	}
	k, _ := registry.OpenKey(registry.CURRENT_USER, l.RunKey, registry.SET_VALUE)
	k.DeleteValue(Name) // switched off in the tray
	k.Close()
	if err := l.Install(release(t, "v2"), "0.4.0", false); err != nil {
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

func TestRegisterRestoresAMissingOrMovedRegistration(t *testing.T) {
	l := testLayout(t)
	if err := l.Install(release(t, "v1"), "0.3.0", true); err != nil {
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
	if err := l.Install(release(t, "v1"), "0.3.0", true); err != nil {
		t.Fatal(err)
	}
	if err := l.Unregister(); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{l.MozillaKey, l.UninstallKey} {
		if key, err := registry.OpenKey(registry.CURRENT_USER, k, registry.QUERY_VALUE); err == nil {
			key.Close()
			t.Errorf("%s is still there", k)
		}
	}
	if value(t, l.RunKey, Name) != "" {
		t.Error("start at login is still on")
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

func TestInstallWithoutTheHostSaysToExtractTheZip(t *testing.T) {
	l := testLayout(t)
	src := release(t, "v1")
	os.Remove(filepath.Join(src, "dms-host.exe"))
	err := l.Install(src, "0.3.0", true)
	if err == nil || !strings.Contains(err.Error(), "zip") {
		t.Fatalf("error %v, want one about extracting the zip", err)
	}
}
