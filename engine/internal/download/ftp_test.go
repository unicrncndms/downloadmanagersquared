package download

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeFTP is an FTP server on loopback, passive mode only, with the switches
// the tests need. It is plain FTP unless tlsCfg is set.
type fakeFTP struct {
	ln           net.Listener
	tlsCfg       *tls.Config
	implicit     bool          // TLS from the first byte, else AUTH TLS
	requireReuse bool          // a data connection must resume the control connection's TLS session
	noRest       bool          // REST is refused
	noEPSV       bool          // only PASV, which names an address nobody should connect to
	maxLogins    int           // 0: no limit
	delay        time.Duration // pause per 64 KiB sent
	dropAt       int64         // the first RETR breaks after this many bytes

	mu         sync.Mutex
	data       []byte
	mtime      string
	active     int
	peak       int
	retrs      int // transfers under way
	retrPeak   int
	refused    int
	rests      []int64
	dropped    bool
	resumed    int
	notResumed int
	user, pass string
	onRetr     func(f *fakeFTP) // called with mu not held
}

func newFakeFTP(t *testing.T, data []byte) *fakeFTP {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeFTP{ln: ln, data: data, mtime: "20260101120000"}
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeFTP) start() {
	go func() {
		for {
			c, err := f.ln.Accept()
			if err != nil {
				return
			}
			go f.session(c)
		}
	}()
}

func (f *fakeFTP) port() string           { _, p, _ := net.SplitHostPort(f.ln.Addr().String()); return p }
func (f *fakeFTP) url(name string) string { return "ftp://127.0.0.1:" + f.port() + "/" + name }

func (f *fakeFTP) session(raw net.Conn) {
	defer raw.Close()
	c := raw
	if f.tlsCfg != nil && f.implicit {
		c = tls.Server(raw, f.tlsCfg)
	}
	rd := bufio.NewReader(c)
	say := func(s string) { io.WriteString(c, s+"\r\n") }
	f.mu.Lock()
	if f.maxLogins > 0 && f.active >= f.maxLogins {
		f.refused++
		f.mu.Unlock()
		say("421 Too many users logged in")
		return
	}
	f.active++
	f.peak = max(f.peak, f.active)
	f.mu.Unlock()
	held := true
	release := func() {
		if held {
			held = false
			f.mu.Lock()
			f.active--
			f.mu.Unlock()
		}
	}
	defer release()
	say("220 fake ftp")
	var pasv net.Listener
	var rest int64
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return
		}
		cmd, arg, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch strings.ToUpper(cmd) {
		case "AUTH":
			if f.tlsCfg == nil {
				say("502 no TLS")
				continue
			}
			say("234 go ahead")
			c = tls.Server(raw, f.tlsCfg)
			rd = bufio.NewReader(c)
		case "USER":
			f.mu.Lock()
			f.user = arg
			f.mu.Unlock()
			say("331 password please")
		case "PASS":
			f.mu.Lock()
			f.pass = arg
			f.mu.Unlock()
			say("230 welcome")
		case "PBSZ", "PROT", "TYPE":
			say("200 ok")
		case "SIZE":
			f.mu.Lock()
			n := len(f.data)
			f.mu.Unlock()
			say("213 " + strconv.Itoa(n))
		case "MDTM":
			f.mu.Lock()
			m := f.mtime
			f.mu.Unlock()
			say("213 " + m)
		case "EPSV", "PASV":
			if strings.ToUpper(cmd) == "EPSV" && f.noEPSV {
				say("500 no EPSV")
				continue
			}
			if pasv != nil {
				pasv.Close()
			}
			if pasv, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
				say("425 no port")
				continue
			}
			port := pasv.Addr().(*net.TCPAddr).Port
			if strings.ToUpper(cmd) == "EPSV" {
				say(fmt.Sprintf("229 Entering Extended Passive Mode (|||%d|)", port))
			} else {
				say(fmt.Sprintf("227 Entering Passive Mode (10,9,8,7,%d,%d)", port>>8, port&255)) // an address that is not ours
			}
		case "REST":
			if f.noRest {
				say("502 REST not implemented")
				continue
			}
			rest, _ = strconv.ParseInt(arg, 10, 64)
			say("350 restarting")
		case "RETR":
			if pasv == nil {
				say("425 no data connection")
				continue
			}
			pasv.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second))
			dc, err := pasv.Accept()
			pasv.Close()
			pasv = nil
			if err != nil {
				say("425 no data connection")
				continue
			}
			say("150 opening data connection")
			f.send(dc, rest, say)
			rest = 0
		case "QUIT":
			release() // before the answer: the next login must find the slot free
			say("221 bye")
			return
		default:
			say("502 not implemented")
		}
	}
}

func (f *fakeFTP) send(dc net.Conn, rest int64, say func(string)) {
	defer dc.Close()
	counted := false
	defer func() {
		if counted {
			f.mu.Lock()
			f.retrs--
			f.mu.Unlock()
		}
	}()
	var w io.Writer = dc
	if f.tlsCfg != nil {
		t := tls.Server(dc, f.tlsCfg)
		if err := t.Handshake(); err != nil {
			say("425 TLS failed")
			return
		}
		f.mu.Lock()
		if t.ConnectionState().DidResume {
			f.resumed++
		} else {
			f.notResumed++
		}
		f.mu.Unlock()
		if f.requireReuse && !t.ConnectionState().DidResume {
			say("522 TLS session reuse required")
			return
		}
		defer t.Close()
		w = t
	}
	f.mu.Lock()
	data := f.data
	f.rests = append(f.rests, rest)
	f.retrs++
	f.retrPeak = max(f.retrPeak, f.retrs)
	counted = true
	first := !f.dropped
	hook := f.onRetr
	f.mu.Unlock()
	if hook != nil {
		hook(f)
	}
	sent := int64(0)
	for off := rest; off < int64(len(data)); {
		n := min(int64(64<<10), int64(len(data))-off)
		if _, err := w.Write(data[off : off+n]); err != nil {
			say("426 transfer aborted")
			return
		}
		off, sent = off+n, sent+n
		if f.dropAt > 0 && first && sent >= f.dropAt {
			f.mu.Lock()
			f.dropped = true
			f.mu.Unlock()
			say("426 connection lost")
			return
		}
		time.Sleep(f.delay)
	}
	say("226 done")
}

// dialLog stands for the interfaces: each dials through its own function, and the addresses are noted.
type dialLog struct {
	mu    sync.Mutex
	by    map[string]int
	addrs map[string]bool
}

func newDialLog() *dialLog { return &dialLog{by: map[string]int{}, addrs: map[string]bool{}} }

func (l *dialLog) iface(tag string, conns int) Iface {
	return Iface{ID: tag, Conns: conns, Client: &http.Client{}, Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		l.mu.Lock()
		l.by[tag]++
		host, _, _ := net.SplitHostPort(addr)
		l.addrs[host] = true
		l.mu.Unlock()
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}
}

func ftpState(id, url, dir string) State {
	return State{ID: id, URL: url, Dir: dir, Size: -1, Status: StatusQueued}
}

func TestFTPSplitOverTwoInterfacesIgnoresPasvAddress(t *testing.T) {
	data := randomData(16 << 20)
	f := newFakeFTP(t, data)
	f.noEPSV, f.delay = true, 10*time.Millisecond
	f.start()
	dl := newDialLog()
	env := &testEnv{ifaces: []Iface{dl.iface("eth", 2), dl.iface("wifi", 2)}, main: "eth"}
	dir := t.TempDir()
	d := runToEnd(t, env, ftpState("f1", f.url("pub/big.bin"), dir))
	if s := d.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
	if dl.by["eth"] == 0 || dl.by["wifi"] == 0 {
		t.Errorf("both interfaces should connect: %v", dl.by)
	}
	if len(dl.addrs) != 1 || !dl.addrs["127.0.0.1"] {
		t.Errorf("only the control server's address may be dialed, got %v", dl.addrs)
	}
	if f.peak < 2 {
		t.Errorf("expected connections side by side, peak %d", f.peak)
	}
	if f.user != "anonymous" {
		t.Errorf("login as %q, want anonymous", f.user)
	}
}

func TestFTPLoginLimitCostsConnectionsNotTheLink(t *testing.T) {
	data := randomData(12 << 20)
	f := newFakeFTP(t, data)
	f.maxLogins, f.delay = 1, 5*time.Millisecond
	f.start()
	dl := newDialLog()
	env := &testEnv{ifaces: []Iface{dl.iface("eth", 3), dl.iface("wifi", 2)}, main: "eth"}
	dir := t.TempDir()
	d := runToEnd(t, env, ftpState("f2", f.url("big.bin"), dir))
	s := d.Summary()
	if s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
	if f.peak != 1 {
		t.Errorf("the limit is one login at a time, peak %d", f.peak)
	}
	for id, l := range s.Links {
		if l.Status == LinkFailed || l.Status == LinkMismatch {
			t.Errorf("link %s is %s: %s", id, l.Status, l.Error)
		}
	}
}

func TestFTPWithoutRestUsesOneConnection(t *testing.T) {
	data := randomData(8 << 20)
	f := newFakeFTP(t, data)
	f.noRest, f.delay = true, 2*time.Millisecond
	f.start()
	dl := newDialLog()
	env := &testEnv{ifaces: []Iface{dl.iface("eth", 3), dl.iface("wifi", 3)}, main: "eth"}
	dir := t.TempDir()
	d := runToEnd(t, env, ftpState("f3", f.url("big.bin"), dir))
	if s := d.Summary(); s.Status != StatusCompleted || s.RangeOK {
		t.Fatalf("status %s (ranges %v): %s", s.Status, s.RangeOK, s.Error)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.retrPeak != 1 {
		t.Errorf("without REST there is one connection, peak %d", f.retrPeak)
	}
	if dl.by["wifi"] != 1 { // its probe only
		t.Errorf("wifi should only probe, dialed %d times", dl.by["wifi"])
	}
}

func TestFTPResumesWithRest(t *testing.T) {
	data := randomData(8 << 20)
	f := newFakeFTP(t, data)
	f.dropAt = 3 << 20
	f.start()
	dl := newDialLog()
	env := &testEnv{ifaces: []Iface{dl.iface("eth", 1)}, main: "eth"}
	dir := t.TempDir()
	d := runToEnd(t, env, ftpState("f4", f.url("big.bin"), dir))
	if s := d.Summary(); s.Status != StatusCompleted {
		t.Fatalf("status %s: %s", s.Status, s.Error)
	}
	checkFile(t, filepath.Join(dir, "big.bin"), data)
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.dropped || len(f.rests) < 2 || f.rests[len(f.rests)-1] == 0 {
		t.Errorf("after the break the rest should be asked for with REST, got %v", f.rests)
	}
}

func TestFTPChangedFileIsCaught(t *testing.T) {
	data := randomData(8 << 20)
	f := newFakeFTP(t, data)
	f.dropAt = 1 << 20 // the connection breaks, and the retry finds another file
	f.onRetr = func(f *fakeFTP) {
		f.mu.Lock()
		f.mtime = "20260202000000" // someone replaced the file once the transfer began
		f.mu.Unlock()
	}
	f.start()
	dl := newDialLog()
	env := &testEnv{ifaces: []Iface{dl.iface("eth", 1)}, main: "eth"}
	dir := t.TempDir()
	d := runToEnd(t, env, ftpState("f5", f.url("big.bin"), dir))
	s := d.Summary()
	if s.Status != StatusFailed || !strings.Contains(s.Error, "changed on the server") {
		t.Fatalf("status %s: %q", s.Status, s.Error)
	}
}

func TestFTPLoginFromTheLinkStaysOutOfIt(t *testing.T) {
	clean, login, err := SplitLogin("ftp://bob:p%40ss%3Aword@example.org/a/b.bin")
	if err != nil || clean != "ftp://example.org/a/b.bin" {
		t.Fatalf("clean %q, %v", clean, err)
	}
	if u, p := ftpLogin(string(login)); u != "bob" || p != "p@ss:word" {
		t.Errorf("login %q %q", u, p)
	}
	if strings.Contains(fmt.Sprint(login), "ss") || strings.Contains(fmt.Sprintf("%v %s", login, login), "bob") {
		t.Errorf("a login must print as hidden, got %v", login)
	}
	if _, _, err := SplitLogin("ftp://:::pw@/x"); err == nil || strings.Contains(err.Error(), "pw") {
		t.Errorf("a bad link's error must not repeat it: %v", err)
	}

	data := randomData(1 << 20)
	f := newFakeFTP(t, data)
	f.start()
	env := &testEnv{ifaces: []Iface{newDialLog().iface("eth", 1)}, main: "eth"}
	dir := t.TempDir()
	st := ftpState("f6", f.url("a.bin"), dir)
	st.Login = login
	d := runToEnd(t, env, st)
	if s := d.Summary(); s.Status != StatusCompleted || strings.Contains(s.URL, "bob") {
		t.Fatalf("status %s, url %q: %s", s.Status, s.URL, s.Error)
	}
	if f.user != "bob" || f.pass != "p@ss:word" {
		t.Errorf("server saw %q %q", f.user, f.pass)
	}
}

// selfSigned is a certificate for 127.0.0.1 and the pool that trusts it.
func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake ftp"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func TestFTPSReusesTheControlSession(t *testing.T) {
	for _, implicit := range []bool{false, true} {
		t.Run(fmt.Sprint("implicit=", implicit), func(t *testing.T) {
			cert, pool := selfSigned(t)
			oldRoots, oldPort := ftpRoots, ftpImplicitPort
			t.Cleanup(func() { ftpRoots, ftpImplicitPort = oldRoots, oldPort })
			ftpRoots = pool
			data := randomData(8 << 20)
			f := newFakeFTP(t, data)
			f.tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
			f.implicit, f.requireReuse, f.delay = implicit, true, 5*time.Millisecond
			if implicit {
				ftpImplicitPort = f.port()
			}
			f.start()
			env := &testEnv{ifaces: []Iface{newDialLog().iface("eth", 2)}, main: "eth"}
			dir := t.TempDir()
			d := runToEnd(t, env, ftpState("f7", "ftps"+strings.TrimPrefix(f.url("big.bin"), "ftp"), dir))
			if s := d.Summary(); s.Status != StatusCompleted {
				t.Fatalf("status %s: %s", s.Status, s.Error)
			}
			checkFile(t, filepath.Join(dir, "big.bin"), data)
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.resumed == 0 || f.notResumed != 0 {
				t.Errorf("data connections should resume the control session: %d did, %d did not", f.resumed, f.notResumed)
			}
		})
	}
}

func TestFTPSUntrustedCertificateFailsTheLink(t *testing.T) {
	cert, _ := selfSigned(t)
	f := newFakeFTP(t, randomData(1<<20))
	f.tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	f.start()
	env := &testEnv{ifaces: []Iface{newDialLog().iface("eth", 1)}, main: "eth"}
	d := New(env, ftpState("f8", "ftps"+strings.TrimPrefix(f.url("big.bin"), "ftp"), t.TempDir()))
	d.Start()
	defer d.Stop(StatusPaused)
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if l := d.Summary().Links["eth"]; l.Status == LinkFailed {
			if !strings.Contains(l.Error, "certificate") {
				t.Errorf("error %q", l.Error)
			}
			return
		}
	}
	t.Fatalf("the link did not fail: %+v", d.Summary().Links["eth"])
}
