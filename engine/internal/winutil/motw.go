package winutil

import (
	"net/url"
	"os"
	"strings"
)

// MarkFromInternet writes the Mark of the Web a browser writes on a download:
// a Zone.Identifier stream with ZoneId=3 (Internet), so SmartScreen, Office
// Protected View and archive tools treat the file as from the internet.
// hostURL and referrer may be "". A volume without alternate data streams
// (FAT32, exFAT) returns an error the caller only logs.
func MarkFromInternet(path, hostURL, referrer string) error {
	var b strings.Builder
	b.WriteString("[ZoneTransfer]\r\nZoneId=3\r\n")
	if r := markURL(referrer); r != "" {
		b.WriteString("ReferrerUrl=" + r + "\r\n")
	}
	if h := markURL(hostURL); h != "" {
		b.WriteString("HostUrl=" + h + "\r\n")
	}
	// A read-only file takes no new stream; it stays read-only after.
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o200 == 0 {
		if err := os.Chmod(path, 0o666); err == nil {
			defer os.Chmod(path, fi.Mode().Perm())
		}
	}
	return os.WriteFile(path+":Zone.Identifier", []byte(b.String()), 0o666)
}

// markURL is u as a mark records it: http(s) only, without a user name or
// password, "" for anything else.
func markURL(u string) string {
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") {
		return ""
	}
	p.User = nil
	return p.String()
}
