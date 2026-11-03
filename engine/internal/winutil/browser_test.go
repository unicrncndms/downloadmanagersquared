package winutil

import "testing"

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
	} {
		if got := browserLaunch(c.exe); got != c.want {
			t.Errorf("browserLaunch(%q) = %v, want %v", c.exe, got, c.want)
		}
	}
}
