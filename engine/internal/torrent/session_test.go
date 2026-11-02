package torrent

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	atorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"

	"downloadmanagersquared/engine/internal/download"
)

// swarm is a set of local seeders for one file, each standing in for a peer on the
// internet at a TEST-NET address that nothing can reach directly.
type swarm struct {
	info    metainfo.Info
	hash    metainfo.Hash
	data    []byte
	addrs   map[string]string // public (TEST-NET) address -> real loopback address
	clients []*atorrent.Client
}

func newSwarm(t *testing.T, seeders int, size int) *swarm {
	t.Helper()
	src := t.TempDir()
	data := make([]byte, size)
	rand.Read(data)
	if err := os.WriteFile(filepath.Join(src, "file.bin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	info := metainfo.Info{PieceLength: 256 << 10}
	if err := info.BuildFromFilePath(filepath.Join(src, "file.bin")); err != nil {
		t.Fatal(err)
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	mi := metainfo.MetaInfo{InfoBytes: infoBytes}
	s := &swarm{info: info, hash: mi.HashInfoBytes(), data: data, addrs: map[string]string{}}
	for i := range seeders {
		cfg := atorrent.NewDefaultClientConfig()
		cfg.SetListenAddr("127.0.0.1:0")
		cfg.Seed = true
		cfg.NoDHT = true
		cfg.DisableUTP = true
		cfg.DisableIPv6 = true
		cfg.NoDefaultPortForwarding = true
		cfg.DisableWebtorrent = true
		cfg.DataDir = src
		stor := storage.NewFileOpts(storage.NewFileClientOpts{
			ClientBaseDir: src, PieceCompletion: storage.NewMapPieceCompletion(),
		})
		cfg.DefaultStorage = stor
		cl, err := atorrent.NewClient(cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cl.Close(); stor.Close() })
		tor, err := cl.AddTorrent(&mi)
		if err != nil {
			t.Fatal(err)
		}
		tor.VerifyData()
		s.clients = append(s.clients, cl)
		s.addrs[fmt.Sprintf("192.0.2.%d:6881", i+1)] = fmt.Sprintf("127.0.0.1:%d", cl.LocalPort())
	}
	return s
}

func (s *swarm) magnet() string {
	m := metainfo.Magnet{InfoHash: s.hash, DisplayName: s.info.Name, Params: map[string][]string{}}
	for public := range s.addrs {
		m.Params.Add("x.pe", public)
	}
	return m.String()
}

// iface reaches the swarm's public addresses by dialing their loopback
// stand-ins, and counts every peer it carried.
func (s *swarm) iface(id string, peers *atomic.Int32) Iface {
	return Iface{
		ID: id,
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			real, ok := s.addrs[addr]
			if !ok || network != "tcp" && network != "tcp4" {
				return nil, errors.New("unreachable")
			}
			c, err := (&net.Dialer{}).DialContext(ctx, "tcp4", real)
			if err == nil {
				peers.Add(1)
			}
			return c, err
		},
		ListenPacket: func() (net.PacketConn, error) { return net.ListenPacket("udp4", "127.0.0.1:0") },
		LookupIP: func(context.Context, string) ([]net.IP, error) {
			return nil, errors.New("no DNS in tests")
		},
	}
}

func waitFor(t *testing.T, what string, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestMagnetDownloadsThroughTheGivenInterfacesOnly(t *testing.T) {
	sw := newSwarm(t, 2, 3<<20)
	var wifi, eth atomic.Int32
	s, err := NewSession(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetIfaces([]Iface{sw.iface("wifi", &wifi), sw.iface("eth", &eth)})

	dir := t.TempDir()
	tor, err := s.Add(State{ID: "t1", Magnet: sw.magnet(), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	tor.Start()
	waitFor(t, "the torrent to complete", 30*time.Second, func() bool {
		return tor.Status() == download.StatusCompleted
	})

	got, err := os.ReadFile(filepath.Join(dir, sw.info.Name))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, sw.data) {
		t.Fatal("downloaded file differs from the seeded one")
	}
	// A finished torrent's files are the user's: nothing keeps them open.
	if err := os.Rename(filepath.Join(dir, sw.info.Name), filepath.Join(dir, "moved.bin")); err != nil {
		t.Fatalf("finished file is still locked: %v", err)
	}
	if st := tor.State(); st.Name != sw.info.Name || st.Size != int64(len(sw.data)) {
		t.Fatalf("state = name %q size %d, want %q %d", st.Name, st.Size, sw.info.Name, len(sw.data))
	}
	// Two seeders, two interfaces: the second peer goes to the interface without one.
	if wifi.Load() == 0 || eth.Load() == 0 {
		t.Fatalf("peers per interface: wifi %d, eth %d; want both used", wifi.Load(), eth.Load())
	}
}

func TestPausedTorrentResumesAndCompletes(t *testing.T) {
	sw := newSwarm(t, 1, 1<<20)
	var peers atomic.Int32
	s, err := NewSession(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetIfaces([]Iface{sw.iface("eth", &peers)})

	dir := t.TempDir()
	tor, err := s.Add(State{ID: "t1", Magnet: sw.magnet(), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	tor.Start()
	tor.Stop(download.StatusPaused)
	if tor.Status() != download.StatusPaused || tor.Running() {
		t.Fatalf("after pause: status %q running %v", tor.Status(), tor.Running())
	}
	// Resuming re-queues a stopped download; the queue then starts it.
	tor.Stop(download.StatusQueued)
	if tor.Status() != download.StatusQueued {
		t.Fatalf("after resume: status %q, want queued", tor.Status())
	}
	tor.Start()
	waitFor(t, "the resumed torrent to complete", 30*time.Second, func() bool {
		return tor.Status() == download.StatusCompleted
	})
}
