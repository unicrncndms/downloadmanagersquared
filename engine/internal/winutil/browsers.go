package winutil

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// Browser is an installed browser the extension runs in.
type Browser struct {
	Name string // as the browser registers itself, e.g. "Firefox Developer Edition"
	Exe  string

	Chromium bool // a Chrome-family browser: it gets the extension from the Chrome Web Store, not the .xpi
}

// firefoxFamily are the exes of browsers that run Firefox extensions and read
// Firefox's native-messaging registration.
var firefoxFamily = []string{"firefox.exe", "librewolf.exe", "waterfox.exe", "floorp.exe"}

// chromiumFamily are the exes of browsers that run Chrome extensions and read
// Chrome's native-messaging registration.
var chromiumFamily = []string{"chrome.exe", "msedge.exe", "brave.exe", "chromium.exe"}

// Browsers are the Firefox- and Chromium-family browsers installed for this
// user or the machine, found as Windows' default-apps settings list browsers.
// The Firefox family comes first, Firefox itself first among them.
func Browsers() []Browser {
	var out []Browser
	seen := map[string]bool{}
	add := func(name, exe string) {
		key := strings.ToLower(filepath.Clean(exe))
		if seen[key] {
			return
		}
		if fi, err := os.Stat(exe); err != nil || fi.IsDir() {
			return
		}
		seen[key] = true
		out = append(out, Browser{strings.TrimPrefix(name, "Mozilla "), exe, isChromiumFamily(exe)})
	}
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		for _, path := range []string{`SOFTWARE\Clients\StartMenuInternet`, `SOFTWARE\WOW6432Node\Clients\StartMenuInternet`} {
			k, err := registry.OpenKey(root, path, registry.ENUMERATE_SUB_KEYS)
			if err != nil {
				continue
			}
			subs, _ := k.ReadSubKeyNames(-1)
			k.Close()
			for _, sub := range subs {
				name := RegString(root, path+`\`+sub, "")
				exe := commandExe(RegString(root, path+`\`+sub+`\shell\open\command`, ""))
				if name == "" {
					name = sub
				}
				if isFirefoxFamily(exe) || isChromiumFamily(exe) {
					add(name, exe)
				}
			}
		}
	}
	// Firefox from the Microsoft Store lives where it can't be started by
	// path, but leaves an alias that can.
	if !slices.ContainsFunc(out, func(b Browser) bool { return strings.EqualFold(filepath.Base(b.Exe), "firefox.exe") }) {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			add("Firefox", filepath.Join(local, `Microsoft\WindowsApps\firefox.exe`))
		}
	}
	sortBrowsers(out)
	return out
}

// sortBrowsers puts the Firefox family first, Firefox itself first of it, the rest by name.
func sortBrowsers(out []Browser) {
	slices.SortStableFunc(out, func(a, b Browser) int {
		if a.Chromium != b.Chromium {
			if a.Chromium {
				return 1
			}
			return -1
		}
		af, bf := strings.EqualFold(filepath.Base(a.Exe), "firefox.exe"), strings.EqualFold(filepath.Base(b.Exe), "firefox.exe")
		switch {
		case af && !bf:
			return -1
		case bf && !af:
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
}

func isFirefoxFamily(exe string) bool {
	return exe != "" && slices.Contains(firefoxFamily, strings.ToLower(filepath.Base(exe)))
}

// IsChromium: exe is a Chrome-family browser's.
func IsChromium(exe string) bool { return isChromiumFamily(exe) }

func isChromiumFamily(exe string) bool {
	return exe != "" && slices.Contains(chromiumFamily, strings.ToLower(filepath.Base(exe)))
}

// commandExe is the program a registered command line starts:
// `"C:\Program Files\Mozilla Firefox\firefox.exe" -osint -url "%1"` gives the
// quoted path, an unquoted one runs to the first space after ".exe".
func commandExe(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if strings.HasPrefix(cmd, `"`) {
		if end := strings.Index(cmd[1:], `"`); end >= 0 {
			return cmd[1 : end+1]
		}
		return ""
	}
	if i := strings.Index(strings.ToLower(cmd), ".exe"); i >= 0 {
		return cmd[:i+4]
	}
	return cmd
}

// RegString is the string value name ("" for the default) of the key at path under root, "" if there is none.
func RegString(root registry.Key, path, name string) string {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, _ := k.GetStringValue(name)
	return v
}
