package download

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Windows error numbers UserText knows. syscall names only some of them.
const (
	errFileNotFound  = syscall.Errno(2)
	errPathNotFound  = syscall.Errno(3)
	errAccessDenied  = syscall.Errno(5)
	errNotReady      = syscall.Errno(21) // a drive pulled or not mounted
	errSharing       = syscall.Errno(32)
	errLockViolation = syscall.Errno(33)
	errHandleFull    = syscall.Errno(39)
	errBadNetPath    = syscall.Errno(53)
	errNetGone       = syscall.Errno(55)
	errNetDeleted    = syscall.Errno(64)
	errDiskFull      = syscall.Errno(112)
	errNameTooLong   = syscall.Errno(206)
	errConnReset     = syscall.Errno(10054)
	errConnAborted   = syscall.Errno(10053)
	errTimedOut      = syscall.Errno(10060)
	errConnRefused   = syscall.Errno(10061)
	errAddrNotAvail  = syscall.Errno(10049) // WSAEADDRNOTAVAIL: "not valid in its context"
	errNetUnreach    = syscall.Errno(10051)
	errHostUnreach   = syscall.Errno(10065)
)

// UserText is err as a card shows it: what happened in plain words, and where
// when that helps (the drive, the server's name). The raw error belongs in
// engine.log, not on a card. An error with no plain wording is returned as it
// is: the engine's own errors are written for the user already.
func UserText(err error) string {
	if err == nil {
		return ""
	}
	if s := diskText(err); s != "" {
		return s
	}
	if s := netText(err); s != "" {
		return s
	}
	return err.Error()
}

// diskText words an error from a file or folder, "" when err is none.
func diskText(err error) string {
	drive := ""
	var pe *fs.PathError
	var le *os.LinkError
	switch {
	case errors.As(err, &pe):
		drive = filepath.VolumeName(pe.Path)
	case errors.As(err, &le):
		drive = filepath.VolumeName(le.New)
	}
	switch {
	case (errors.Is(err, errDiskFull) || errors.Is(err, errHandleFull)) && drive != "":
		return "Drive " + drive + " is full: free some space, then resume"
	case errors.Is(err, errDiskFull) || errors.Is(err, errHandleFull):
		return "The disk is full: free some space, then resume"
	case errors.Is(err, errNotReady) || errors.Is(err, errNetGone) || errors.Is(err, errBadNetPath) || errors.Is(err, errNetDeleted):
		if drive != "" {
			return "Drive " + drive + " is not available: connect it again, then resume"
		}
		return "The drive is not available: connect it again, then resume"
	case errors.Is(err, errSharing) || errors.Is(err, errLockViolation):
		return "Another program has the file open: close it, then resume"
	case errors.Is(err, errAccessDenied) || errors.Is(err, fs.ErrPermission):
		return "Windows refused access to the file: check the folder's permissions or your antivirus, then resume"
	case errors.Is(err, errNameTooLong):
		return "The file's path is too long for Windows: choose a shorter folder"
	case (errors.Is(err, errPathNotFound) || errors.Is(err, errFileNotFound)) && pe != nil:
		return "A file or folder it needs is gone: " + pe.Path
	}
	return ""
}

// netText words an error from a connection to the server, "" when err is none.
func netText(err error) string {
	var dns *net.DNSError
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	var verify *tls.CertificateVerificationError
	var rec tls.RecordHeaderError
	var ne net.Error
	switch {
	case errors.As(err, &dns) && dns.IsNotFound:
		return "The server's name " + dns.Name + " can't be found: check the link, or this connection's DNS"
	case errors.As(err, &dns):
		return "Looking up the server's name " + dns.Name + " failed on this connection"
	case errors.As(err, &unknown) || errors.As(err, &host) || errors.As(err, &invalid) || errors.As(err, &verify):
		return "The server's certificate isn't trusted, so nothing was sent to it"
	case errors.As(err, &rec):
		return "The server didn't answer as a secure (HTTPS) server"
	case errors.Is(err, errConnRefused):
		return "The server refused the connection"
	case errors.Is(err, errAddrNotAvail):
		// Seen with a VPN app's split tunnel excluding dms-engine.exe: it reroutes the
		// app's sockets, and one bound to another interface's address is refused.
		return "Windows refused to connect from this connection's address. If it stays so, another program reroutes DMS²'s connections, often a VPN app's split tunnel: take dms-engine.exe out of it"
	case errors.Is(err, errNetUnreach) || errors.Is(err, errHostUnreach):
		return "No route to the server over this connection"
	case errors.Is(err, errConnReset) || errors.Is(err, errConnAborted):
		return "The server or the network cut the connection"
	case errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF):
		return "The server closed the connection early"
	case errors.Is(err, errTimedOut) || errors.Is(err, os.ErrDeadlineExceeded) || errors.As(err, &ne) && ne.Timeout():
		return "The server didn't answer in time"
	case strings.Contains(err.Error(), "tls:"):
		return "The secure (HTTPS) connection to the server failed"
	}
	return ""
}
