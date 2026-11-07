package torrent

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/download"
)

// newSwarm6 is newSwarm with its seeders on ::1, reachable only over IPv6.
func newSwarm6(t *testing.T, seeders, size int) *swarm {
	t.Helper()
	if ln, err := net.Listen("tcp6", "[::1]:0"); err != nil {
		t.Skip("no IPv6 loopback: ", err)
	} else {
		ln.Close()
	}
	src := t.TempDir()
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i * 7)
	}
	if err := os.WriteFile(filepath.Join(src, "file.bin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	s := newSwarmOn(t, seeders, src, "file.bin", false, true)
	s.data = data
	return s
}

// iface6 is sw.iface for an IPv6-only interface: no IPv4 socket, IPv6 ones on ::1.
func (sw *swarm) iface6(id string, peers *atomic.Int32) Iface {
	ifc := sw.iface(id, peers)
	ifc.ListenPacket = func(int) (net.PacketConn, error) { return nil, errors.New("this connection has no IPv4") }
	ifc.ListenPacket6 = func(port int) (net.PacketConn, error) {
		return net.ListenPacket("udp6", net.JoinHostPort("::1", strconv.Itoa(port)))
	}
	return ifc
}

// An IPv6-only interface fetches a torrent from peers on IPv6, with its uTP
// and DHT over IPv6.
func TestATorrentComesFromIPv6PeersOverAnIPv6OnlyInterface(t *testing.T) {
	sw := newSwarm6(t, 1, 2<<20)
	var peers atomic.Int32
	s, err := NewSession(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetIfaces([]Iface{sw.iface6("phone", &peers)})
	dir := t.TempDir()
	tor, err := s.Add(State{ID: "t6", Magnet: sw.magnet(), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	tor.Start()
	waitFor(t, "the interface's sockets", 5*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.ifs) > 0
	})
	s.mu.Lock()
	is := s.ifs[0]
	s.mu.Unlock()
	if is.v4 != nil || is.v6 == nil || is.v6.utp.Addr().(*net.UDPAddr).IP.To4() != nil {
		t.Errorf("sockets: v4 %v, v6 %v; want IPv6 only", is.v4, is.v6)
	}
	waitFor(t, "the torrent to complete over IPv6", 30*time.Second, func() bool {
		return tor.Status() == download.StatusCompleted
	})
	got, err := os.ReadFile(filepath.Join(dir, sw.info.Name))
	if err != nil || !bytes.Equal(got, sw.data) {
		t.Fatalf("file: %d bytes, %v", len(got), err)
	}
	if peers.Load() == 0 {
		t.Fatal("no peer went through the interface")
	}
}

// With IPv6 off (no IPv6 sockets to give), no IPv6 peer is dialled and no
// IPv6 socket opened, as before.
func TestWithoutIPv6NoIPv6PeerIsTried(t *testing.T) {
	sw := newSwarm6(t, 1, 1<<20)
	var peers atomic.Int32
	s, err := NewSession(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetIfaces([]Iface{sw.iface("eth", &peers)})
	tor := inSwarm(t, s, sw)
	time.Sleep(3 * time.Second)
	if n := peers.Load(); n != 0 || tor.Status() == download.StatusCompleted {
		t.Fatalf("%d IPv6 peers dialled with IPv6 off", n)
	}
	s.mu.Lock()
	is := s.ifs[0]
	s.mu.Unlock()
	if is.v6 != nil || s.ipv6 {
		t.Error("IPv6 sockets with IPv6 off")
	}
}
