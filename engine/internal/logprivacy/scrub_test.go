package logprivacy

import (
	"testing"
)

// engine.log can be sent as it is: no URL's path or query, no secret, no
// public address, no file name, while what tells lines apart stays.
func TestScrubKeepsTheUsersOwnOutOfTheLog(t *testing.T) {
	data := `C:\Users\Robin\AppData\Roaming\downloadmanagersquared`
	for _, c := range []struct{ in, want string }{
		{`download ab: Get "https://u:pw@cdn.example.com:8443/files/My%20Movie.mkv?token=abc": EOF`, `download ab: Get "https://cdn.example.com:8443/…": EOF`},
		{`tunnel for x: Post "https://www.privateinternetaccess.com/api/client/v2/token": dial tcp`, `tunnel for x: Post "https://www.privateinternetaccess.com/…": dial tcp`},
		{`resolver: https://example.com/ via eth`, `resolver: https://example.com via eth`},
		{`added magnet:?xt=urn:btih:abc&dn=secret`, `added magnet:…`},
		{`download ab: naming the part: rename F:\Dowmloads\Mafia.Definitive.Edition-CPY.part7.rar.dms.part F:\Dowmloads\Mafia.Definitive.Edition-CPY.part7.rar: The process cannot access the file`,
			`download ab: naming the part: rename F:\…\x.part7.rar.dms.part F:\…\x.part7.rar: The process cannot access the file`},
		{`download ab: starting over, its partial file is gone: D:\My Games\big file.iso.dms.part`, `download ab: starting over, its partial file is gone: D:\…\x.iso.dms.part`},
		{`set ab: extracted to E:\Spiele\Neu Ordner`, `set ab: extracted to E:\…`},
		{`setting aside ` + data + `\settings.json: access denied`, `setting aside %DATA%\settings.json: access denied`},
		{`interface WLAN 2 (Intel AX211) 192.168.188.28 10.62.208.211 100.70.1.2 95.90.208.111 fe80::1 2a02:8108:1:2::5, socks port 1080`,
			`interface WLAN 2 (Intel AX211) 192.168.188.28 10.62.208.211 100.70.1.2 <public IPv4> fe80::1 <public IPv6>, socks port 1080`},
		{`dial tcp 141.95.207.211:443: i/o timeout`, `dial tcp <public IPv4>:443: i/o timeout`},
		{`a cookie: sessionid=abc; password=hunter2`, `a cookie: …; password=…`},
		{`engine 1.0.0 starting`, `engine 1.0.0 starting`},
		{`ipc: getState took 3s`, `ipc: getState took 3s`},
		{`download ab failed: writing Mafia Game/data/Mafia.Definitive.Edition.part2.rar: There is not enough space`, `download ab failed: writing Mafia …/x.part2.rar: There is not enough space`},
		{`resolver: hoster.example via WLAN 2: replay step 1 of 2 (a "Free Download")`, `resolver: hoster.example via WLAN 2: replay step 1 of 2 (a "Free Download")`},
	} {
		if got := Scrub(c.in, data, Standard); got != c.want {
			t.Errorf("Scrub(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// Log privacy at Increased also leaves out the sites and every file name,
// but the engine's own files.
func TestStrictScrubLeavesOutSitesAndNames(t *testing.T) {
	data := `C:\Users\Robin\AppData\Roaming\downloadmanagersquared`
	for _, c := range []struct{ in, want string }{
		{`download ab: Get "https://cdn.example.com:8443/files/a.mkv?t=1": EOF`, `download ab: Get "https://…": EOF`},
		{`resolver: hoster.example.com via WLAN 2: replay step 1 of 2 (a "Mafia.part1.rar")`, `resolver: x.com via WLAN 2: replay step 1 of 2 (a "x.part1.rar")`},
		{`dial tcp: lookup mega.nz: no such host`, `dial tcp: lookup x.nz: no such host`},
		{`setting aside ` + data + `\settings.json: access denied`, `setting aside %DATA%\settings.json: access denied`},
		{`crash.log: access denied`, `crash.log: access denied`},
		{`engine 1.0.0 starting, 10.62.208.211`, `engine 1.0.0 starting, 10.62.208.211`},
		{`context deadline exceeded (Client.Timeout exceeded while awaiting headers)`, `context deadline exceeded (Client.Timeout exceeded while awaiting headers)`},
	} {
		if got := Scrub(c.in, data, Strict); got != c.want {
			t.Errorf("Scrub(%q, strict)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// Log privacy Off: the line as written, as before there was a scrub.
func TestScrubOffLeavesTheLineAsItIs(t *testing.T) {
	in := `download ab: Get "https://cdn.example.com/a.mkv?t=1" via 95.90.208.111`
	if got := Scrub(in, "", Off); got != in {
		t.Fatalf("scrub off changed %q to %q", in, got)
	}
}

// At every level DMS²'s own code stays: its pages' script errors and its crash
// traces; a network share and the data folder in any case are scrubbed too.
func TestScrubKeepsOwnCodeAndScrubsSharesAndDataInAnyCase(t *testing.T) {
	data := `C:\Users\Robin\AppData\Roaming\downloadmanagersquared`
	page := "window page: TypeError: x is undefined\nrender@https://dms.invalid/ui/card.js:120:7"
	crash := "\tD:/a/dms/dms/engine/internal/download/set.go:412 +0x1a4"
	for _, l := range []Level{Standard, Strict} {
		for _, in := range []string{page, crash} {
			if got := Scrub(in, data, l); got != in {
				t.Errorf("level %d changed own code %q to %q", l, in, got)
			}
		}
		share := `download ab: no sparse file in \\NAS\share\Filme\Big Movie.mkv: not supported`
		if got, want := Scrub(share, data, l), `download ab: no sparse file in \\…\x.mkv: not supported`; got != want {
			t.Errorf("level %d share: got %q, want %q", l, got, want)
		}
		lower := `setting aside c:\users\robin\appdata\roaming\downloadmanagersquared\settings.json: denied`
		if got, want := Scrub(lower, data, l), `setting aside %DATA%\settings.json: denied`; got != want {
			t.Errorf("level %d data folder: got %q, want %q", l, got, want)
		}
	}
}
