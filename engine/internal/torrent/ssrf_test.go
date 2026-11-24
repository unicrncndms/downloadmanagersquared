package torrent

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestPublicOnly(t *testing.T) {
	look := func(_ context.Context, h string) ([]net.IP, error) {
		switch h {
		case "pub.example":
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		case "rebind.example":
			return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("10.0.0.1")}, nil
		}
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	var dialed string
	dial := func(_ context.Context, _, a string) (net.Conn, error) { dialed = a; return nil, errors.New("stub") }
	d := publicOnly(look, dial)
	for _, a := range []string{"127.0.0.1:80", "[::1]:80", "10.1.2.3:80", "172.16.0.1:80", "192.168.1.1:80",
		"169.254.169.254:80", "100.64.0.1:80", "0.0.0.0:80", "[::ffff:127.0.0.1]:80", "localhost:80", "rebind.example:80"} {
		if _, err := d(context.Background(), "tcp", a); !errors.Is(err, errInternalTarget) {
			t.Errorf("%s: err = %v, want refused", a, err)
		}
	}
	d(context.Background(), "tcp", "pub.example:443")
	if dialed != "93.184.216.34:443" {
		t.Errorf("dialed %q, want the resolved address", dialed)
	}
}

func TestIsOtherLine(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	s := &Session{}
	a, b := &ifaceSockets{}, &ifaceSockets{v4: &familySockets{tcp: ln}}
	s.ifs = []*ifaceSockets{a, b}
	// utp is nil in b: only the TCP address is compared
	b.v4.utp = nil
	addr := ln.Addr().String()
	if !s.isOtherLine(a, false, addr) {
		t.Error("another line's address not refused")
	}
	if s.isOtherLine(b, false, addr) {
		t.Error("a line's own address refused")
	}
	if s.isOtherLine(a, false, "127.0.0.1:1") {
		t.Error("foreign address refused")
	}
}
