package download

// SFTP downloads: one SSH session per connection, each reading its chunk at an
// offset with pkg/sftp. The TCP connection is dialed with the interface's own
// dial function, so nothing leaves the chosen interface or tunnel. Host keys
// are trusted on first use: an unknown key is asked about before any login is
// sent, a changed one fails the link.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// IsSFTP: the link is an sftp:// one.
func IsSFTP(u string) bool { return strings.HasPrefix(strings.ToLower(u), "sftp://") }

// HostKeyAsk is an unknown server key waiting for the user's yes: the download shows it.
type HostKeyAsk struct {
	Host        string `json:"host"`        // host:port
	Fingerprint string `json:"fingerprint"` // SHA256:...
	Key         string `json:"key"`         // base64 wire format, what Trust stores
}

type hostKeyError struct {
	ask     HostKeyAsk
	changed string // the trusted key's fingerprint when the server's differs
}

func (e *hostKeyError) Error() string {
	if e.changed != "" {
		return fmt.Sprintf("WARNING: the host key of %s changed (trusted %s, server now sends %s). Not logging in: someone may be intercepting the connection, or the server was reinstalled. Only if you are sure, forget the trusted key under Settings > SFTP logins; the next link asks again.", e.ask.Host, e.changed, e.ask.Fingerprint)
	}
	return fmt.Sprintf("unknown host key %s for %s: trust it?", e.ask.Fingerprint, e.ask.Host)
}

func fingerprint(k ssh.PublicKey) string {
	if k == nil {
		return "(unreadable key)"
	}
	h := sha256.Sum256(k.Marshal())
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(h[:])
}

// SplitSFTP takes the login out of an sftp:// link. The link keeps the user (it
// says which saved login to use) but not the password. keyFile, with its
// passphrase, is the other way to log in. login is "" when the link had neither.
func SplitSFTP(raw, keyFile, keyPass string) (clean, user, hostport string, login Secret, err error) {
	u, perr := url.Parse(raw)
	if perr != nil || u.Hostname() == "" { // perr's text would repeat the link, password and all
		return "", "", "", "", errors.New("not a usable SFTP link")
	}
	if u.User == nil || u.User.Username() == "" {
		return "", "", "", "", errors.New("an SFTP link needs a user name: sftp://user@host/path")
	}
	user = u.User.Username()
	if pw, ok := u.User.Password(); ok {
		login = Secret("p" + pw)
	}
	if keyFile != "" {
		login = Secret("k" + keyFile + "\n" + keyPass)
	}
	u.User = url.User(user)
	port := u.Port()
	if port == "" {
		port = "22"
	}
	return u.String(), user, net.JoinHostPort(u.Hostname(), port), login, nil
}

func sftpTarget(raw string) (hostport, user, name string, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User == nil {
		return "", "", "", errors.New("not a usable SFTP link")
	}
	port := u.Port()
	if port == "" {
		port = "22"
	}
	name = u.Path
	if name == "" || strings.HasSuffix(name, "/") {
		return "", "", "", errors.New("this link is a folder, not a file")
	}
	return net.JoinHostPort(u.Hostname(), port), u.User.Username(), name, nil
}

// sftpKeyParses counts key files parsed, for the tests.
var sftpKeyParses atomic.Int64

// keyCache holds a download's parsed key files by login, so its connections
// read a key, and decrypt a passphrase's bcrypt, once.
type keyCache struct {
	mu sync.Mutex
	m  map[string]ssh.Signer
}

// sftpAuth builds the login methods; keys may be nil.
func sftpAuth(login string, keys *keyCache) ([]ssh.AuthMethod, error) {
	if login == "" {
		return nil, errors.New("no saved login for this user and host: add the link again with a password or a key file")
	}
	body := login[1:]
	if login[0] == 'p' {
		return []ssh.AuthMethod{ssh.Password(body), ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
			as := make([]string, len(qs))
			for i := range as {
				as[i] = body
			}
			return as, nil
		})}, nil
	}
	if keys != nil {
		keys.mu.Lock() // held through the parse: a second connection waits for the first's result
		defer keys.mu.Unlock()
		if sg, ok := keys.m[login]; ok {
			return []ssh.AuthMethod{ssh.PublicKeys(sg)}, nil
		}
	}
	file, pass, _ := strings.Cut(body, "\n")
	pem, err := os.ReadFile(file)
	if err != nil {
		return nil, errors.New("the key file can't be read: " + path.Base(strings.ReplaceAll(file, `\`, "/")))
	}
	sftpKeyParses.Add(1)
	var signer ssh.Signer
	if pass != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(pass))
	} else {
		signer, err = ssh.ParsePrivateKey(pem)
	}
	if err != nil {
		return nil, errors.New("the key file is not usable (wrong passphrase?)")
	}
	if keys != nil {
		if keys.m == nil {
			keys.m = map[string]ssh.Signer{}
		}
		keys.m[login] = signer
	}
	return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
}

type sftpConn struct {
	ssh  *ssh.Client
	cl   *sftp.Client
	f    *sftp.File // the file, open for as long as the session is kept
	stop func() bool
}

func (c *sftpConn) close() {
	c.stop()
	c.cl.Close()
	c.ssh.Close()
}

// sftpDial connects, checks the host key before any login is sent, logs in and opens the SFTP subsystem.
func sftpDial(ctx context.Context, dial dialFunc, hostport, user string, keys *keyCache) (*sftpConn, error) {
	if dial == nil || SFTP == nil {
		return nil, errors.New("no way out for SFTP on this connection")
	}
	auth, err := sftpAuth(string(SFTP.Login(hostport, user)), keys)
	if err != nil {
		return nil, &fetchError{kind: kindRange, msg: err.Error()}
	}
	raw, err := dial(ctx, "tcp", hostport)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	cfg := &ssh.ClientConfig{User: user, Auth: auth, Timeout: ftpTimeout, HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
		ask := HostKeyAsk{Host: hostport, Fingerprint: fingerprint(k), Key: base64.StdEncoding.EncodeToString(k.Marshal())}
		trusted := SFTP.HostKey(hostport)
		switch {
		case len(trusted) == 0:
			return &hostKeyError{ask: ask}
		case string(trusted) != string(k.Marshal()):
			old, _ := ssh.ParsePublicKey(trusted) // a stored key that no longer parses counts as changed
			return &hostKeyError{ask: ask, changed: fingerprint(old)}
		}
		return nil
	}}
	raw.SetDeadline(time.Now().Add(ftpTimeout))
	cc, chans, reqs, err := ssh.NewClientConn(raw, hostport, cfg)
	if err != nil {
		stop()
		raw.Close()
		return nil, err
	}
	raw.SetDeadline(time.Time{})
	sc := ssh.NewClient(cc, chans, reqs)
	cl, err := sftp.NewClient(sc, sftp.UseConcurrentReads(true))
	if err != nil {
		stop()
		sc.Close()
		return nil, err
	}
	return &sftpConn{ssh: sc, cl: cl, stop: stop}, nil
}

// sftpFetchError sorts an SFTP failure for the download: busy (a session or
// start limit, which costs a connection), given up on (a host key, a login that
// is refused), the file gone. A network error stays as it is.
func sftpFetchError(err error) error {
	var hk *hostKeyError
	var fe *fetchError
	var oc *ssh.OpenChannelError
	var st *sftp.StatusError
	msg := err.Error()
	switch {
	case errors.As(err, &fe):
		return fe
	case errors.As(err, &hk):
		return &fetchError{kind: kindRange, msg: hk.Error()}
	case errors.As(err, &oc):
		return &fetchError{kind: kindBusy, msg: "server busy: " + msg, retryAfter: 15 * time.Second}
	case errors.Is(err, os.ErrNotExist):
		return &fetchError{kind: kindChanged, msg: "the file is not on the server"}
	case errors.As(err, &st):
		return &fetchError{kind: kindHTTP, msg: "SFTP: " + msg}
	case strings.Contains(msg, "unable to authenticate"):
		return &fetchError{kind: kindRange, msg: "the server refused the login: " + msg}
	case strings.Contains(msg, "handshake failed"): // MaxStartups drops the connection before the key exchange ends
		return &fetchError{kind: kindBusy, msg: "server busy: " + msg, retryAfter: 15 * time.Second}
	}
	return err
}

func sftpVersion(fi os.FileInfo) Version {
	return Version{LastModified: fi.ModTime().UTC().Format(http.TimeFormat)}
}

// probeSFTP asks for the file's size and version. Everything but a network
// error is an answer; an unknown host key comes back as hostKey.
func probeSFTP(ctx context.Context, dial dialFunc, l Link) (probeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	hostport, user, name, err := sftpTarget(l.URL)
	if err != nil {
		return probeResult{fail: err.Error()}, nil
	}
	answer := func(err error) (probeResult, error) {
		var hk *hostKeyError
		if errors.As(err, &hk) && hk.changed == "" {
			return probeResult{fail: hk.Error(), hostKey: &hk.ask}, nil
		}
		var fe *fetchError
		if !errors.As(sftpFetchError(err), &fe) {
			return probeResult{}, err
		}
		switch fe.kind {
		case kindBusy:
			return probeResult{status: 503, msg: fe.msg, wait: fe.retryAfter}, nil
		case kindRange:
			return probeResult{fail: fe.msg}, nil
		}
		return probeResult{status: 550, msg: fe.msg}, nil
	}
	c, err := sftpDial(ctx, dial, hostport, user, nil)
	if err != nil {
		return answer(err)
	}
	defer c.close()
	fi, err := c.cl.Stat(name)
	if err != nil {
		return answer(err)
	}
	if fi.IsDir() {
		return probeResult{fail: "this link is a folder, not a file"}, nil
	}
	return probeResult{status: 200, size: fi.Size(), name: SanitizeFilename(path.Base(name)), version: sftpVersion(fi), rangeOK: true}, nil
}

// openSFTP is a chunk of an SFTP file. The worker keeps its session and the
// open file from chunk to chunk; each chunk checks the file with one fstat, then
// seeks and streams. A stale kept session is replaced once, silently. ctx is the
// worker's (the session lives as long), rctx the chunk's.
func (w *worker) openSFTP(ctx, rctx context.Context, start, end int64, ranged bool, size int64, ver Version) (io.ReadCloser, error) {
	hostport, user, name, err := sftpTarget(w.link.URL)
	if err != nil {
		return nil, &fetchError{kind: kindRange, msg: err.Error()}
	}
	for reused := w.sftp != nil; ; reused = false {
		b, err := w.trySFTP(ctx, rctx, hostport, user, name, start, end, ranged, size, ver)
		var fe *fetchError
		if err != nil && reused && !(errors.As(err, &fe) && fe.kind == kindChanged) {
			continue
		}
		return b, err
	}
}

func (w *worker) trySFTP(ctx, rctx context.Context, hostport, user, name string, start, end int64, ranged bool, size int64, ver Version) (io.ReadCloser, error) {
	c := w.sftp
	if c == nil {
		var err error
		if c, err = sftpDial(ctx, w.dial, hostport, user, &w.d.keys); err != nil {
			return nil, sftpFetchError(err)
		}
		if c.f, err = c.cl.Open(name); err != nil {
			c.close()
			return nil, sftpFetchError(err)
		}
		w.sftp = c
	}
	fail := func(err error) (io.ReadCloser, error) {
		w.dropSFTP()
		return nil, err
	}
	fi, err := c.f.Stat()
	if err != nil {
		return fail(sftpFetchError(err))
	}
	if ranged && (size >= 0 && fi.Size() != size || !sftpVersion(fi).same(ver)) {
		return fail(&fetchError{kind: kindChanged, msg: versionMsg})
	}
	if !ranged {
		start = 0
	}
	n := end - start
	if !ranged || end == unknownEnd {
		n = 1<<62 - start
	}
	if _, err := c.f.Seek(start, io.SeekStart); err != nil {
		return fail(sftpFetchError(err))
	}
	// WriteTo reads ahead with many requests in flight; the pipe's writer ends it at the chunk's end.
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := c.f.WriteTo(&capWriter{w: pw, n: n})
		if err == errChunkEnd {
			err = nil
		}
		pw.CloseWithError(err)
	}()
	// A stalled chunk closes the session; the worker ends on that error.
	return &sftpBody{pr: pr, done: done, stop: context.AfterFunc(rctx, c.close)}, nil
}

func (w *worker) dropSFTP() {
	if w.sftp != nil {
		w.sftp.close()
		w.sftp = nil
	}
}

var errChunkEnd = errors.New("chunk end")

// capWriter passes on n bytes, then stops the WriteTo that feeds it.
type capWriter struct {
	w io.Writer
	n int64
}

func (c *capWriter) Write(p []byte) (int, error) {
	if int64(len(p)) >= c.n {
		k, err := c.w.Write(p[:c.n])
		if err == nil {
			err = errChunkEnd
		}
		c.n -= int64(k)
		return k, err
	}
	c.n -= int64(len(p))
	return c.w.Write(p)
}

type sftpBody struct {
	pr   *io.PipeReader
	done chan struct{}
	stop func() bool
}

func (b *sftpBody) Read(p []byte) (int, error) { return b.pr.Read(p) }

// Close waits for the read-ahead to wind down, so the file is free for the next chunk's seek.
func (b *sftpBody) Close() error {
	b.pr.Close()
	<-b.done
	b.stop()
	return nil
}

// TrustHostKey is the user's yes to the server key the download asked about:
// it is kept, and the links that failed on it try again.
func (d *Download) TrustHostKey() error {
	d.mu.Lock()
	ask := d.st.HostAsk
	d.mu.Unlock()
	if ask == nil || SFTP == nil {
		return errors.New("no host key is waiting for trust")
	}
	key, err := base64.StdEncoding.DecodeString(ask.Key)
	if err != nil {
		return err
	}
	if err := SFTP.TrustHostKey(ask.Host, key); err != nil {
		return err
	}
	d.mu.Lock()
	d.st.HostAsk = nil
	for _, l := range d.st.Links {
		l.retry()
	}
	d.mu.Unlock()
	d.Resume()
	return nil
}
