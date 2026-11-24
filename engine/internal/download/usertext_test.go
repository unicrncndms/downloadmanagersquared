package download

import (
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
)

// A card never shows a raw Go error for the errors Windows and the network
// commonly give: it says what happened, where, and what to do.
func TestUserTextWordsCommonErrors(t *testing.T) {
	path := func(p string, e error) error { return &fs.PathError{Op: "write", Path: p, Err: e} }
	dial := func(e error) error {
		return &url.Error{Op: "Get", URL: "https://x.example/f", Err: &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connectex", e)}}
	}
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("could not finish writing the file: %w", path(`E:\dl\a.iso.dms.part`, errDiskFull)), "Drive E: is full: free some space, then resume"},
		{path(`F:\a.part`, errNotReady), "Drive F: is not available: connect it again, then resume"},
		{path(`C:\a.part`, errSharing), "Another program has the file open: close it, then resume"},
		{path(`C:\a.part`, errAccessDenied), "Windows refused access to the file: check the folder's permissions or your antivirus, then resume"},
		{dial(errConnRefused), "The server refused the connection"},
		{dial(errTimedOut), "The server didn't answer in time"},
		{dial(errConnReset), "The server or the network cut the connection"},
		{dial(errNetUnreach), "No route to the server over this connection"},
		{dial(errAddrNotAvail), "Windows refused to connect from this connection's address. If it stays so, another program reroutes DMS²'s connections, often a VPN app's split tunnel: take dms-engine.exe out of it"},
		{&url.Error{Op: "Get", URL: "u", Err: &net.DNSError{Name: "x.example", Err: "no such host", IsNotFound: true}}, "The server's name x.example can't be found: check the link, or this connection's DNS"},
		{&url.Error{Op: "Get", URL: "u", Err: x509.UnknownAuthorityError{}}, "The server's certificate isn't trusted, so nothing was sent to it"},
		{fmt.Errorf("read: %w", io.ErrUnexpectedEOF), "The server closed the connection early"},
		{errChanged, errChanged.Error()}, // the engine's own words stay
	}
	for _, c := range cases {
		if got := UserText(c.err); got != c.want {
			t.Errorf("UserText(%v)\n got %q\nwant %q", c.err, got, c.want)
		}
		if got := shortErr(c.err); c.err != errChanged && got != c.want {
			t.Errorf("shortErr(%v) = %q", c.err, got)
		}
	}
	if s := UserText(errors.New("odd")); s != "odd" {
		t.Errorf("unknown error: %q", s)
	}
	if strings.Contains(diskMsg(path(`C:\a`, os.ErrClosed)), "Writing to disk failed") == false {
		t.Error("an unknown write error isn't said to be one")
	}
}
