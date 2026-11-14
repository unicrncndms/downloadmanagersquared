package download

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// fakeSSH is an SSH server with an SFTP subsystem on loopback, serving one folder.
type fakeSSH struct {
	ln          net.Listener
	hostKey     ssh.Signer
	dir         string
	password    string
	clientKey   ssh.PublicKey
	maxSessions int // 0: no limit

	mu     sync.Mutex
	auths  int // login attempts that reached the server
	active int
	peak   int
}

func newSigner(t *testing.T) (ssh.Signer, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s, priv
}

func newFakeSSH(t *testing.T, data []byte) *fakeSSH {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hk, _ := newSigner(t)
	f := &fakeSSH{ln: ln, hostKey: hk, dir: t.TempDir()}
	path := filepath.Join(f.dir, "big.bin")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(path, time.Now(), time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeSSH) start() {
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			f.mu.Lock()
			f.auths++
			f.mu.Unlock()
			if f.password != "" && c.User() == "bob" && string(pw) == f.password {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			f.mu.Lock()
			f.auths++
			f.mu.Unlock()
			if f.clientKey != nil && c.User() == "bob" && string(k.Marshal()) == string(f.clientKey.Marshal()) {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
	}
	cfg.AddHostKey(f.hostKey)
	go func() {
		for {
			c, err := f.ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c, cfg)
		}
	}()
}

func (f *fakeSSH) serve(c net.Conn, cfg *ssh.ServerConfig) {
	defer c.Close()
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		f.mu.Lock()
		if nc.ChannelType() != "session" || f.maxSessions > 0 && f.active >= f.maxSessions {
			f.mu.Unlock()
			nc.Reject(ssh.ResourceShortage, "too many sessions")
			continue
		}
		f.active++
		f.peak = max(f.peak, f.active)
		f.mu.Unlock()
		ch, creqs, err := nc.Accept()
		if err != nil {
			return
		}
		go func() {
			defer func() { f.mu.Lock(); f.active--; f.mu.Unlock() }()
			for r := range creqs {
				ok := r.Type == "subsystem" && len(r.Payload) > 4 && string(r.Payload[4:]) == "sftp"
				r.Reply(ok, nil)
				if ok {
					srv, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(f.dir))
					if err == nil {
						srv.Serve()
						srv.Close()
					}
					return
				}
			}
		}()
	}
}

func (f *fakeSSH) hostPort() string { return f.ln.Addr().String() }
func (f *fakeSSH) url(user string) string {
	return "sftp://" + user + "@" + f.hostPort() + f.path()
}

// path is the file's path as an SFTP URL carries it: absolute, and with a drive letter on Windows ("/C:/...").
func (f *fakeSSH) path() string {
	return "/" + strings.TrimPrefix(filepath.ToSlash(filepath.Join(f.dir, "big.bin")), "/")
}
func (f *fakeSSH) trust(t *testing.T) {
	t.Helper()
	if err := SFTP.TrustHostKey(f.hostPort(), f.hostKey.PublicKey().Marshal()); err != nil {
		t.Fatal(err)
	}
}

func useSFTPStore(t *testing.T) {
	t.Helper()
	old := SFTP
	SFTP = OpenSFTPStore(filepath.Join(t.TempDir(), "sftp.json"))
	t.Cleanup(func() { SFTP = old })
}

func waitSFTP(t *testing.T, d *Download, ok func(Summary) bool) Summary {
	t.Helper()
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if s := d.Summary(); ok(s) {
			return s
		}
	}
	t.Fatalf("not reached: %+v", d.Summary())
	return Summary{}
}

func TestSFTPPasswordSplitOverTwoInterfaces(t *testing.T) {
	useSFTPStore(t)
	data := randomData(8 << 20)
	f := newFakeSSH(t, data)
	f.password = "s3cret"
	f.start()
	f.trust(t)
	clean, user, host, login, err := SplitSFTP("sftp://bob:s3cret@"+f.hostPort()+f.path(), "", "")
	if err != nil || strings.Contains(clean, "s3cret") || user != "bob" {
		t.Fatalf("clean %q %v", clean, err)
	}
	SFTP.SaveLogin(host, user, login)
	dl := newDialLog()
	env := &testEnv{ifaces: []Iface{dl.iface("eth", 2), dl.iface("wifi", 2)}, main: "eth"}
	dir := t.TempDir()
	d := runToEnd(t, env, ftpState("s1", clean, dir))
	if s := d.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
	if dl.by["eth"] == 0 || dl.by["wifi"] == 0 {
		t.Errorf("both interfaces should connect: %v", dl.by)
	}
	if f.peak < 2 {
		t.Errorf("expected sessions side by side, peak %d", f.peak)
	}
}

func TestSFTPKeyLogin(t *testing.T) {
	useSFTPStore(t)
	data := randomData(2 << 20)
	f := newFakeSSH(t, data)
	signer, priv := newSigner(t)
	f.clientKey = signer.PublicKey()
	f.start()
	f.trust(t)
	blk, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("pp"))
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "id_ed25519")
	os.WriteFile(keyFile, pem.EncodeToMemory(blk), 0o600)
	clean, user, host, login, err := SplitSFTP(f.url("bob"), keyFile, "pp")
	if err != nil {
		t.Fatal(err)
	}
	SFTP.SaveLogin(host, user, login)
	env := &testEnv{ifaces: []Iface{newDialLog().iface("eth", 2)}, main: "eth"}
	dir := t.TempDir()
	d := runToEnd(t, env, ftpState("s2", clean, dir))
	if s := d.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
}

func TestSFTPUnknownHostKeyAsksBeforeAnyLogin(t *testing.T) {
	useSFTPStore(t)
	data := randomData(1 << 20)
	f := newFakeSSH(t, data)
	f.password = "pw"
	f.start()
	clean, user, host, login, _ := SplitSFTP("sftp://bob:pw@"+f.hostPort()+f.path(), "", "")
	SFTP.SaveLogin(host, user, login)
	env := &testEnv{ifaces: []Iface{newDialLog().iface("eth", 2)}, main: "eth"}
	dir := t.TempDir()
	d := New(env, ftpState("s3", clean, dir))
	env.finished = make(chan struct{})
	d.Start()
	s := waitSFTP(t, d, func(s Summary) bool { return s.HostKey != nil })
	if s.HostKey.Fingerprint != fingerprint(f.hostKey.PublicKey()) || !strings.HasPrefix(s.HostKey.Fingerprint, "SHA256:") {
		t.Errorf("asked about %+v", s.HostKey)
	}
	f.mu.Lock()
	auths := f.auths
	f.mu.Unlock()
	if auths != 0 {
		t.Errorf("%d login attempts before the key was trusted", auths)
	}
	if err := d.TrustHostKey(); err != nil {
		t.Fatal(err)
	}
	d.Start() // the queue does this in the engine
	select {
	case <-env.finished:
	case <-time.After(30 * time.Second):
		t.Fatalf("no finish after trust: %+v", d.Summary())
	}
	if s := d.Summary(); s.Status != StatusCompleted || s.HostKey != nil {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
}

func TestSFTPChangedHostKeyFailsAndNeverAsks(t *testing.T) {
	useSFTPStore(t)
	f := newFakeSSH(t, randomData(1<<20))
	f.password = "pw"
	f.start()
	other, _ := newSigner(t)
	SFTP.TrustHostKey(f.hostPort(), other.PublicKey().Marshal())
	clean, user, host, login, _ := SplitSFTP("sftp://bob:pw@"+f.hostPort()+f.path(), "", "")
	SFTP.SaveLogin(host, user, login)
	env := &testEnv{ifaces: []Iface{newDialLog().iface("eth", 1)}, main: "eth"}
	d := New(env, ftpState("s4", clean, t.TempDir()))
	d.Start()
	defer d.Stop(StatusPaused)
	l := waitSFTP(t, d, func(s Summary) bool { return s.Links["eth"].Status == LinkFailed }).Links["eth"]
	if !strings.Contains(l.Error, "changed") || d.Summary().HostKey != nil {
		t.Errorf("error %q, ask %v", l.Error, d.Summary().HostKey)
	}
	if f.auths != 0 {
		t.Errorf("%d login attempts to a server with a changed key", f.auths)
	}
}

func TestSFTPSessionLimitCostsConnectionsNotTheLink(t *testing.T) {
	useSFTPStore(t)
	data := randomData(6 << 20)
	f := newFakeSSH(t, data)
	f.password, f.maxSessions = "pw", 1
	f.start()
	f.trust(t)
	clean, user, host, login, _ := SplitSFTP("sftp://bob:pw@"+f.hostPort()+f.path(), "", "")
	SFTP.SaveLogin(host, user, login)
	env := &testEnv{ifaces: []Iface{newDialLog().iface("eth", 3), newDialLog().iface("wifi", 2)}, main: "eth"}
	dir := t.TempDir()
	d := runToEnd(t, env, ftpState("s5", clean, dir))
	s := d.Summary()
	if s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
	if f.peak != 1 {
		t.Errorf("the limit is one session at a time, peak %d", f.peak)
	}
	for id, l := range s.Links {
		if l.Status == LinkFailed || l.Status == LinkMismatch {
			t.Errorf("link %s is %s: %s", id, l.Status, l.Error)
		}
	}
}

func TestSFTPStoreSealsLoginsAndRemovesThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sftp.json")
	s := OpenSFTPStore(path)
	s.SaveLogin("h:22", "bob", Secret("phunter2"))
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "hunter2") {
		t.Fatalf("login written readable: %s", raw)
	}
	if got := OpenSFTPStore(path).Login("h:22", "bob"); got != "phunter2" {
		t.Errorf("read back %q", got)
	}
	if l := s.Logins(); len(l) != 1 || l[0].User != "bob" || l[0].Key {
		t.Errorf("list %+v", l)
	}
	s.RemoveLogin("h:22", "bob")
	if len(OpenSFTPStore(path).Logins()) != 0 {
		t.Error("login not removed")
	}
}

func TestSFTPStoreListsAndForgetsHostKeys(t *testing.T) {
	useSFTPStore(t)
	k, _ := newSigner(t)
	SFTP.TrustHostKey("b.example:22", k.PublicKey().Marshal())
	SFTP.TrustHostKey("a.example:2222", k.PublicKey().Marshal())
	l := SFTP.HostKeys()
	if len(l) != 2 || l[0].Host != "a.example:2222" || l[0].Fingerprint != fingerprint(k.PublicKey()) {
		t.Fatalf("listed %+v", l)
	}
	if err := SFTP.ForgetHostKey("a.example:2222"); err != nil || len(SFTP.HostKey("a.example:2222")) != 0 || len(SFTP.HostKeys()) != 1 {
		t.Fatalf("not forgotten: %v", err)
	}
	if again := OpenSFTPStore(SFTP.path); len(again.HostKeys()) != 1 {
		t.Fatal("forgetting was not saved")
	}
}
