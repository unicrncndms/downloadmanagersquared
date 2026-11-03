package socks

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/winutil"
)

// fakePath records what the server dials and answers with one end of a pipe.
type fakePath struct {
	mu     sync.Mutex
	dialed []string
	names  map[string][]net.IP
}

func (p *fakePath) path() Path {
	return Path{
		Dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			p.mu.Lock()
			p.dialed = append(p.dialed, addr)
			p.mu.Unlock()
			a, b := net.Pipe()
			go io.Copy(io.Discard, b)
			return a, nil
		},
		Lookup: func(_ context.Context, host string) ([]net.IP, error) {
			if ips, ok := p.names[host]; ok {
				return ips, nil
			}
			return nil, errors.New("no such host")
		},
	}
}

func listen(t *testing.T, p *fakePath) *Server {
	t.Helper()
	s, err := Listen(p.path())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// connect asks the server for a CONNECT to host:443 and returns its reply
// code, or -1 if it closed the connection without one.
func connect(t *testing.T, s *Server, host string) int {
	t.Helper()
	c, err := net.Dial("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(s.Port())))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		return -1
	}
	var m [2]byte
	if _, err := io.ReadFull(c, m[:]); err != nil {
		return -1
	}
	req := []byte{5, 1, 0}
	if ip := net.ParseIP(host).To4(); ip != nil {
		req = append(append(req, 1), ip...)
	} else {
		req = append(append(req, 3, byte(len(host))), host...)
	}
	req = binary.BigEndian.AppendUint16(req, 443)
	c.Write(req)
	var rep [10]byte
	if _, err := io.ReadFull(c, rep[:]); err != nil {
		return -1
	}
	return int(rep[1])
}

func TestAClientOfTheSameUserGetsThrough(t *testing.T) {
	p := &fakePath{}
	s := listen(t, p)
	if code := connect(t, s, "93.184.215.14"); code != repSucceeded {
		t.Fatalf("reply %d, want success", code)
	}
	if len(p.dialed) != 1 || p.dialed[0] != "93.184.215.14:443" {
		t.Fatalf("dialed %v", p.dialed)
	}
}

func TestClientsOfOtherUsersAreClosed(t *testing.T) {
	old := clientOwner
	t.Cleanup(func() { clientOwner = old })
	for _, fake := range []func(client, server *net.TCPAddr) (uint32, string, error){
		func(_, _ *net.TCPAddr) (uint32, string, error) { return 4242, "S-1-5-21-1-2-3-1001", nil },
		func(_, _ *net.TCPAddr) (uint32, string, error) { return 0, "", errors.New("no row in the TCP table") },
	} {
		clientOwner = fake
		p := &fakePath{}
		s := listen(t, p)
		if code := connect(t, s, "93.184.215.14"); code != -1 {
			t.Fatalf("reply %d, want the connection closed", code)
		}
		if len(p.dialed) != 0 {
			t.Fatalf("dialed %v for another user", p.dialed)
		}
	}
}

func TestOnlyPublicTargetsAreDialled(t *testing.T) {
	p := &fakePath{names: map[string][]net.IP{
		"router.home":   {net.IPv4(192, 168, 1, 1)},
		"mixed.example": {net.IPv4(10, 1, 2, 3), net.IPv4(93, 184, 215, 14)},
		"example.com":   {net.IPv4(93, 184, 215, 14)},
	}}
	s := listen(t, p)
	for _, target := range []string{"10.0.0.1", "127.0.0.1", "169.254.169.254", "100.64.0.1", "router.home"} {
		if code := connect(t, s, target); code != repNotAllowed {
			t.Errorf("%s: reply %d, want %d (not allowed)", target, code, repNotAllowed)
		}
	}
	if len(p.dialed) != 0 {
		t.Fatalf("dialed %v", p.dialed)
	}
	for _, target := range []string{"example.com", "mixed.example"} {
		if code := connect(t, s, target); code != repSucceeded {
			t.Errorf("%s: reply %d, want success", target, code)
		}
	}
	if len(p.dialed) != 2 || p.dialed[0] != "93.184.215.14:443" || p.dialed[1] != "93.184.215.14:443" {
		t.Fatalf("dialed %v, want only the public address", p.dialed)
	}
}

func TestTheOwnProcessOwnsItsLoopbackConnection(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, user, err := clientOwner(c.LocalAddr().(*net.TCPAddr), ln.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	if me, _ := winutil.CurrentUserSID(); user != me {
		t.Fatalf("owner %s, want %s", user, me)
	}
}
