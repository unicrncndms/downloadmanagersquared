package winutil

import (
	"strings"
	"testing"
)

func TestBrowserLaunchByPath(t *testing.T) {
	for _, c := range []struct {
		exe  string
		want launch
	}{
		{"", launchDefault},
		{`C:\Program Files\LibreWolf\librewolf.exe`, launchExe},
		{`D:\PortableApps\FirefoxPortable\App\Firefox64\firefox.exe`, launchExe},
		// Store Firefox can't be started by path: Windows opens it as the default browser.
		{`C:\Program Files\WindowsApps\Mozilla.Firefox_140.0.0.0_x64__n80bbvh6b1yt2\VFS\ProgramFiles\Firefox Package Root\firefox.exe`, launchDefault},
		// Its alias can.
		{`C:\Users\x\AppData\Local\Microsoft\WindowsApps\firefox.exe`, launchExe},
	} {
		if got := browserLaunch(c.exe); got != c.want {
			t.Errorf("browserLaunch(%q) = %v, want %v", c.exe, got, c.want)
		}
	}
}

func TestCommandExe(t *testing.T) {
	for cmd, want := range map[string]string{
		`"C:\Program Files\Mozilla Firefox\firefox.exe" -osint -url "%1"`: `C:\Program Files\Mozilla Firefox\firefox.exe`,
		`C:\Program Files\LibreWolf\librewolf.exe -osint -url "%1"`:       `C:\Program Files\LibreWolf\librewolf.exe`,
		`  "C:\Floorp\floorp.exe"`:                                        `C:\Floorp\floorp.exe`,
		`"unterminated`:                                                   "",
		``:                                                                "",
	} {
		if got := commandExe(cmd); got != want {
			t.Errorf("commandExe(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestFirefoxFamilyByExe(t *testing.T) {
	for exe, want := range map[string]bool{
		`C:\Program Files\Mozilla Firefox\firefox.exe`:          true,
		`C:\Program Files\Waterfox\Waterfox.exe`:                true,
		`C:\Program Files\Google\Chrome\Application\chrome.exe`: false,
		`C:\Program Files\Mozilla Thunderbird\thunderbird.exe`:  false,
		``: false,
	} {
		if got := isFirefoxFamily(exe); got != want {
			t.Errorf("isFirefoxFamily(%q) = %v", exe, got)
		}
	}
}

func TestBrowsersFindsOnlyExistingKnownFamilies(t *testing.T) {
	for _, b := range Browsers() {
		if !(isFirefoxFamily(b.Exe) || isChromiumFamily(b.Exe)) || b.Chromium != isChromiumFamily(b.Exe) || b.Name == "" {
			t.Errorf("found %+v", b)
		}
		t.Logf("found %s at %s", b.Name, b.Exe)
	}
}

func TestChromiumFamilyByExe(t *testing.T) {
	for exe, want := range map[string]bool{
		`C:\Program Files\Google\Chrome\Application\chrome.exe`:        true,
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`: true,
		`C:\Program Files\Mozilla Firefox\firefox.exe`:                 false,
		``: false,
	} {
		if got := isChromiumFamily(exe); got != want {
			t.Errorf("isChromiumFamily(%q) = %v", exe, got)
		}
	}
}

func TestSortBrowsersFirefoxFamilyThenChromium(t *testing.T) {
	got := []Browser{
		{"Microsoft Edge", `C:\e\msedge.exe`, true},
		{"Waterfox", `C:\w\waterfox.exe`, false},
		{"Google Chrome", `C:\c\chrome.exe`, true},
		{"Floorp", `C:\f\floorp.exe`, false},
		{"Firefox", `C:\x\firefox.exe`, false},
	}
	sortBrowsers(got)
	var names []string
	for _, b := range got {
		names = append(names, b.Name)
	}
	if want := "Firefox Floorp Waterfox Google Chrome Microsoft Edge"; strings.Join(names, " ") != want {
		t.Errorf("order = %v, want %s", names, want)
	}
}
