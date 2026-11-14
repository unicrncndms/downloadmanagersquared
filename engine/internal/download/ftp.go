package download

// FTP and FTPS: a small client of our own (RFC 959, 2428, 3659, 4217), just
// what a download needs. Passive mode only, and the data connection always goes
// to the address the control connection reached, never to the one a PASV reply
// names: nothing leaves the chosen interface or tunnel, and no FXP bounce is
// possible. Every connection is dialed with the interface's own dial function.

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// ftpRoots are the certificate authorities FTPS trusts; nil is the system's. Tests set their own.
var ftpRoots *x509.CertPool

const ftpTimeout = 30 * time.Second

// ftpImplicitPort is the port where ftps:// means TLS from the first byte; on any other port it means AUTH TLS. A var for tests.
var ftpImplicitPort = "990"

// IsFTP: the link is an ftp:// or ftps:// one.
func IsFTP(u string) bool {
	u = strings.ToLower(u)
	return strings.HasPrefix(u, "ftp://") || strings.HasPrefix(u, "ftps://")
}

// SplitLogin takes the login out of an FTP link: the link without it, and
// "user:password" (as URL userinfo, escaped) to be kept encrypted. The link is
// what is saved and shown, so the password stays out of both.
func SplitLogin(raw string) (clean string, login Secret, err error) {
	u, perr := url.Parse(raw)
	if perr != nil || u.Hostname() == "" { // perr's text would repeat the link, password and all
		return "", "", errors.New("not a usable FTP link")
	}
	if u.User != nil {
		login = Secret(u.User.String())
		u.User = nil
	}
	return u.String(), login, nil
}

// ftpLogin is the user and password to send: anonymous unless the link had a login.
func ftpLogin(login string) (user, pass string) {
	if login == "" {
		return "anonymous", "anonymous@"
	}
	u, p, _ := strings.Cut(login, ":")
	user, _ = url.PathUnescape(u)
	pass, _ = url.PathUnescape(p)
	return user, pass
}

// ftpName is the file's path as the server wants it: relative to the login
// folder, as RFC 1738 says (ftp://host//abs is absolute).
func ftpName(u *url.URL) (string, error) {
	p := strings.TrimPrefix(u.Path, "/")
	if p == "" || strings.HasSuffix(p, "/") {
		return "", errors.New("this link is a folder, not a file")
	}
	return p, nil
}

type ftpReply struct {
	code int
	text string
}

func (e *ftpReply) Error() string { return fmt.Sprintf("FTP %d %s", e.code, e.text) }

type ftpConn struct {
	raw    net.Conn // the control connection as dialed
	ctl    net.Conn // raw, or its TLS
	rd     *bufio.Reader
	remote string // the server's address: data connections go here
	dial   dialFunc
	tls    *tls.Config // nil: plain FTP, no TLS on data connections either
	stop   func() bool
}

// ftpDial connects, secures the connection when the link says ftps, and logs in.
// ftps:// is implicit TLS on port 990, or explicit (AUTH TLS) on any other port.
func ftpDial(ctx context.Context, dial dialFunc, u *url.URL, login string) (*ftpConn, error) {
	if dial == nil {
		return nil, errors.New("no way out for FTP on this connection")
	}
	secure := strings.EqualFold(u.Scheme, "ftps")
	port := u.Port()
	if port == "" {
		port = "21"
		if secure {
			port = ftpImplicitPort
		}
	}
	raw, err := dial(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return nil, err
	}
	c := &ftpConn{raw: raw, ctl: raw, rd: bufio.NewReaderSize(raw, 4096), dial: dial}
	c.stop = context.AfterFunc(ctx, func() { raw.Close() })
	c.remote = raw.RemoteAddr().String()
	if a, ok := raw.RemoteAddr().(*net.TCPAddr); ok {
		c.remote = a.IP.String()
	} else if h, _, err := net.SplitHostPort(c.remote); err == nil {
		c.remote = h
	}
	fail := func(err error) (*ftpConn, error) {
		raw.Close()
		c.stop()
		return nil, err
	}
	if secure {
		// One config for the control and every data connection: the data ones resume the control's session, which many servers insist on.
		c.tls = &tls.Config{ServerName: u.Hostname(), RootCAs: ftpRoots, MinVersion: tls.VersionTLS12, ClientSessionCache: tls.NewLRUClientSessionCache(8)}
	}
	implicit := secure && port == ftpImplicitPort
	if implicit {
		if err := c.startTLS(ctx); err != nil {
			return fail(err)
		}
	}
	if r, err := c.reply(); err != nil {
		return fail(err)
	} else if r.code != 220 {
		return fail(r)
	}
	if secure && !implicit {
		if _, err := c.want(2, "AUTH TLS"); err != nil {
			return fail(err)
		}
		if err := c.startTLS(ctx); err != nil {
			return fail(err)
		}
	}
	if secure {
		for _, cmd := range []string{"PBSZ 0", "PROT P"} {
			if _, err := c.want(2, "%s", cmd); err != nil {
				return fail(err)
			}
		}
	}
	user, pass := ftpLogin(login)
	r, err := c.cmd("USER %s", user)
	if err == nil && r.code == 331 {
		r, err = c.cmd("PASS %s", pass)
	}
	if err != nil {
		return fail(err)
	}
	if r.code/100 != 2 {
		return fail(r)
	}
	if _, err := c.want(2, "TYPE I"); err != nil {
		return fail(err)
	}
	return c, nil
}

// startTLS runs the handshake on the control connection and reads on over it.
func (c *ftpConn) startTLS(ctx context.Context) error {
	t := tls.Client(c.raw, c.tls)
	c.raw.SetDeadline(time.Now().Add(ftpTimeout))
	if err := t.HandshakeContext(ctx); err != nil {
		return err
	}
	c.ctl, c.rd = t, bufio.NewReaderSize(t, 4096)
	return nil
}

// reply reads one reply, multi-line ones to their last line.
func (c *ftpConn) reply() (*ftpReply, error) {
	c.ctl.SetDeadline(time.Now().Add(ftpTimeout))
	var r ftpReply
	var lines []string
	for range 200 {
		b, err := c.rd.ReadSlice('\n')
		if err != nil {
			if err == bufio.ErrBufferFull {
				err = errors.New("FTP reply too long")
			} else if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
		line := strings.TrimRight(string(b), "\r\n")
		if r.code == 0 {
			n, err := strconv.Atoi(line[:min(3, len(line))])
			if err != nil || len(line) < 3 {
				return nil, errors.New("not an FTP reply")
			}
			r.code = n
			lines = append(lines, strings.TrimPrefix(line[3:], "-"))
			if len(line) == 3 || line[3] != '-' {
				break
			}
			continue
		}
		if strings.HasPrefix(line, strconv.Itoa(r.code)) && (len(line) == 3 || line[3] == ' ') {
			lines = append(lines, strings.TrimSpace(line[min(4, len(line)):]))
			break
		}
		lines = append(lines, strings.TrimSpace(line))
	}
	r.text = strings.TrimSpace(strings.Join(lines, " "))
	return &r, nil
}

// cmd sends one command and reads its reply. Any reply is a success here.
func (c *ftpConn) cmd(format string, a ...any) (*ftpReply, error) {
	line := fmt.Sprintf(format, a...)
	if strings.ContainsAny(line, "\r\n") {
		return nil, errors.New("a line break in an FTP command") // a path could smuggle in a second command
	}
	c.ctl.SetDeadline(time.Now().Add(ftpTimeout))
	if _, err := io.WriteString(c.ctl, line+"\r\n"); err != nil {
		return nil, err
	}
	return c.reply()
}

// want is cmd, and a reply of another class (2xx, 3xx...) is the error.
func (c *ftpConn) want(class int, format string, a ...any) (*ftpReply, error) {
	r, err := c.cmd(format, a...)
	if err != nil {
		return nil, err
	}
	if r.code/100 != class {
		return r, r
	}
	return r, nil
}

// quit ends the session and waits briefly for the server to say it let go, so
// the login it held is free again for the next connection.
func (c *ftpConn) quit() {
	c.stop()
	c.ctl.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(c.ctl, "QUIT\r\n"); err == nil {
		for {
			if r, err := c.reply(); err != nil || r.code == 221 {
				break
			}
		}
	}
	c.raw.Close()
}

// stat is the file's size (-1 when the server can't say) and modification time in HTTP date form ("" when it can't).
func (c *ftpConn) stat(name string) (size int64, mtime string, err error) {
	size = -1
	r, err := c.cmd("SIZE %s", name)
	if err != nil {
		return 0, "", err
	}
	switch {
	case r.code == 213:
		if n, err := strconv.ParseInt(strings.TrimSpace(r.text), 10, 64); err == nil && n >= 0 {
			size = n
		}
	case r.code == 550 || r.code/100 == 4:
		return 0, "", r
	}
	if r, err := c.cmd("MDTM %s", name); err == nil && r.code == 213 {
		t, perr := time.Parse("20060102150405", strings.TrimSpace(r.text)[:min(14, len(strings.TrimSpace(r.text)))])
		if perr == nil {
			mtime = t.UTC().Format(http.TimeFormat)
		}
	}
	return size, mtime, nil
}

var (
	pasvRE = regexp.MustCompile(`(\d+),(\d+),(\d+),(\d+),(\d+),(\d+)`)
	epsvRE = regexp.MustCompile(`\([^0-9]{3}(\d+)[^0-9]\)`) // (|||port|)
)

// passive opens a data connection to the control connection's server, on the
// port its EPSV (else PASV) reply names. The address in a PASV reply is ignored.
func (c *ftpConn) passive(ctx context.Context) (net.Conn, error) {
	port := 0
	if r, err := c.cmd("EPSV"); err != nil {
		return nil, err
	} else if m := epsvRE.FindStringSubmatch(r.text); r.code == 229 && m != nil {
		port, _ = strconv.Atoi(m[1])
	}
	if port == 0 {
		r, err := c.want(2, "PASV")
		if err != nil {
			return nil, err
		}
		m := pasvRE.FindStringSubmatch(r.text)
		if m == nil {
			return nil, errors.New("FTP passive reply not understood")
		}
		hi, _ := strconv.Atoi(m[5])
		lo, _ := strconv.Atoi(m[6])
		port = hi<<8 | lo
	}
	if port <= 0 || port > 65535 {
		return nil, errors.New("FTP passive port out of range")
	}
	return c.dial(ctx, "tcp", net.JoinHostPort(c.remote, strconv.Itoa(port)))
}

// retr starts sending name from byte rest on and returns the data connection.
func (c *ftpConn) retr(ctx context.Context, name string, rest int64) (net.Conn, error) {
	data, err := c.passive(ctx)
	if err != nil {
		return nil, err
	}
	if rest > 0 {
		if _, err = c.want(3, "REST %d", rest); err != nil {
			data.Close()
			return nil, err
		}
	}
	if _, err = c.want(1, "RETR %s", name); err != nil {
		data.Close()
		return nil, err
	}
	if c.tls != nil {
		t := tls.Client(data, c.tls)
		data.SetDeadline(time.Now().Add(ftpTimeout))
		if err = t.HandshakeContext(ctx); err != nil {
			data.Close()
			return nil, err
		}
		data.SetDeadline(time.Time{})
		return t, nil
	}
	return data, nil
}

var tooManyRE = regexp.MustCompile(`(?i)too many|maximum|limit|exceed|no more|\bfull\b|\bmax\b`)

// ftpFetchError sorts an FTP failure for the download: busy (a login limit,
// which costs a connection), given up on (a certificate that isn't trusted),
// the file gone, or another refusal. A network error stays as it is.
func ftpFetchError(err error) error {
	var rep *ftpReply
	var cert *tls.CertificateVerificationError
	switch {
	case errors.As(err, &cert):
		return &fetchError{kind: kindRange, msg: "the server's certificate is not trusted: " + cert.Err.Error()}
	case !errors.As(err, &rep):
		return err
	case rep.code == 421, rep.code == 530 && tooManyRE.MatchString(rep.text):
		return &fetchError{kind: kindBusy, msg: "server busy: " + rep.Error(), retryAfter: 15 * time.Second}
	case rep.code == 550:
		return &fetchError{kind: kindChanged, msg: "the file is not on the server (" + rep.Error() + ")"}
	}
	return &fetchError{kind: kindHTTP, msg: rep.Error()}
}

// probeFTP asks for the file's size and version, and whether REST works. A
// login limit or a refusal is an answer (status, msg); only a network error is an error.
func probeFTP(ctx context.Context, dial dialFunc, l Link, login string) (probeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	u, err := url.Parse(l.URL)
	if err != nil {
		return probeResult{}, errors.New("not a usable FTP link")
	}
	name, err := ftpName(u)
	if err != nil {
		return probeResult{fail: err.Error()}, nil
	}
	answer := func(err error) (probeResult, error) {
		var fe *fetchError
		if !errors.As(ftpFetchError(err), &fe) {
			return probeResult{}, err
		}
		switch fe.kind {
		case kindBusy:
			return probeResult{status: 503, msg: fe.msg, wait: fe.retryAfter}, nil
		case kindRange:
			return probeResult{fail: fe.msg}, nil
		}
		return probeResult{status: 550, msg: fe.msg}, nil // not a rejected link: no landing page to ask for
	}
	c, err := ftpDial(ctx, dial, u, login)
	if err != nil {
		return answer(err)
	}
	defer c.quit()
	size, mtime, err := c.stat(name)
	if err != nil {
		return answer(err)
	}
	r := probeResult{status: 200, size: size, name: SanitizeFilename(path.Base(name)), version: Version{LastModified: mtime}}
	if size > 0 {
		rep, err := c.cmd("REST 1")
		r.rangeOK = err == nil && rep.code == 350
	}
	return r, nil
}

// openFTP is a chunk of an FTP file: a control connection of its own, a check
// that the file is still the one being downloaded, then REST and RETR. Its
// Close ends the transfer (reading only the chunk's bytes) and the session.
func (w *worker) openFTP(ctx context.Context, start, end int64, ranged bool, size int64, ver Version, login string) (io.ReadCloser, error) {
	u, err := url.Parse(w.link.URL)
	if err != nil {
		return nil, &fetchError{kind: kindRange, msg: "not a usable FTP link"}
	}
	name, err := ftpName(u)
	if err != nil {
		return nil, &fetchError{kind: kindRange, msg: err.Error()}
	}
	c, err := ftpDial(ctx, w.dial, u, login)
	if err != nil {
		return nil, ftpFetchError(err)
	}
	if ranged {
		gotSize, mtime, err := c.stat(name)
		if err != nil {
			c.quit()
			return nil, ftpFetchError(err)
		}
		if size >= 0 && gotSize >= 0 && gotSize != size || !(Version{LastModified: mtime}).same(ver) {
			c.quit()
			return nil, &fetchError{kind: kindChanged, msg: versionMsg}
		}
	}
	if !ranged {
		start = 0
	}
	data, err := c.retr(ctx, name, start)
	if err != nil {
		c.quit()
		return nil, ftpFetchError(err)
	}
	b := &ftpBody{r: data, data: data, c: c}
	if ranged && end != unknownEnd {
		b.r = io.LimitReader(data, end-start)
	}
	b.stop = context.AfterFunc(ctx, func() { data.Close() })
	return b, nil
}

type ftpBody struct {
	r    io.Reader
	data net.Conn
	c    *ftpConn
	stop func() bool
}

func (b *ftpBody) Read(p []byte) (int, error) { return b.r.Read(p) }

func (b *ftpBody) Close() error {
	b.stop()
	b.data.Close()
	b.c.quit()
	return nil
}
