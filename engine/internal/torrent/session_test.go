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
	"slices"
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

// infoBytes is the swarm's bencoded info dict, as a State keeps it once known.
func (s *swarm) infoBytes(t *testing.T) []byte {
	t.Helper()
	b, err := bencode.Marshal(s.info)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// leecher joins the swarm as a client with no data, reachable at a public
// address like the seeders, and returns its torrent.
func (s *swarm) leecher(t *testing.T, public string) *atorrent.Torrent {
	t.Helper()
	dir := t.TempDir()
	cfg := atorrent.NewDefaultClientConfig()
	cfg.SetListenAddr("127.0.0.1:0")
	cfg.NoDHT = true
	cfg.DisableUTP = true
	cfg.DisableIPv6 = true
	cfg.NoDefaultPortForwarding = true
	cfg.DisableWebtorrent = true
	stor := storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir: dir, PieceCompletion: storage.NewMapPieceCompletion(),
	})
	cfg.DefaultStorage = stor
	cl, err := atorrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close(); stor.Close() })
	tor, _ := cl.AddTorrentInfoHash(s.hash)
	if err := tor.SetInfoBytes(s.infoBytes(t)); err != nil {
		t.Fatal(err)
	}
	tor.DownloadAll()
	s.addrs[public] = fmt.Sprintf("127.0.0.1:%d", cl.LocalPort())
	return tor
}

func TestCompleteTorrentChecksItsDataThenSeedsToTheRatioTarget(t *testing.T) {
	sw := newSwarm(t, 0, 1<<20)
	leech := sw.leecher(t, "192.0.2.50:6881")
	var peers atomic.Int32
	s, err := NewSession(Config{Ratio: func() float64 { return 1 }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetIfaces([]Iface{sw.iface("eth", &peers)})

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sw.info.Name), sw.data, 0o644); err != nil {
		t.Fatal(err)
	}
	tor, err := s.Add(State{ID: "t1", Magnet: sw.magnet(), Info: sw.infoBytes(t), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	tor.Start()
	waitFor(t, "the torrent to finish at ratio 1", 30*time.Second, func() bool {
		return tor.Status() == download.StatusCompleted
	})
	sum := tor.Summary()
	if sum.Uploaded < int64(len(sw.data)) || sum.Ratio < 1 {
		t.Fatalf("finished with uploaded %d ratio %.2f, want at least %d and 1", sum.Uploaded, sum.Ratio, len(sw.data))
	}
	if leech.BytesCompleted() != int64(len(sw.data)) {
		t.Fatalf("leecher has %d of %d bytes", leech.BytesCompleted(), len(sw.data))
	}
}

func TestHalfDoneTorrentChecksItsDataAndKeepsIt(t *testing.T) {
	sw := newSwarm(t, 0, 1<<20) // no peers: whatever is done came from the disk
	s, err := NewSession(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	dir := t.TempDir()
	half := slices.Clone(sw.data)
	clear(half[len(half)/2:])
	// An unfinished file waits under .part until it is complete.
	if err := os.WriteFile(filepath.Join(dir, sw.info.Name+".part"), half, 0o644); err != nil {
		t.Fatal(err)
	}
	tor, err := s.Add(State{ID: "t1", Magnet: sw.magnet(), Info: sw.infoBytes(t), Dir: dir, Status: download.StatusQueued})
	if err != nil {
		t.Fatal(err)
	}
	tor.Start()
	waitFor(t, "the check to find the first half", 10*time.Second, func() bool {
		s := tor.Summary()
		return s.Status == download.StatusRunning && s.Done == int64(len(sw.data)/2)
	})
	if !tor.Running() {
		t.Fatal("a downloading torrent holds no queue slot")
	}
}

func TestDownloadedTorrentSeedsInsteadOfFinishing(t *testing.T) {
	sw := newSwarm(t, 1, 1<<20)
	var peers atomic.Int32
	s, err := NewSession(Config{Ratio: func() float64 { return 1 }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetIfaces([]Iface{sw.iface("eth", &peers)})
	tor, err := s.Add(State{ID: "t1", Magnet: sw.magnet(), Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	tor.Start()
	waitFor(t, "the downloaded torrent to seed", 30*time.Second, func() bool { return tor.Status() == StatusSeeding })
	if tor.Running() {
		t.Fatal("a seeding torrent holds a queue slot")
	}
	if st := tor.State(); len(st.Info) == 0 || st.Finished.IsZero() {
		t.Fatalf("seeding torrent kept info %d bytes, finished %v; want its info and completion time", len(st.Info), st.Finished)
	}
	// Stopping a seeding torrent (as on a removal) leaves the swarm.
	tor.Stop(download.StatusPaused)
	if tor.Status() != download.StatusPaused {
		t.Fatalf("stopped seeding torrent is %q", tor.Status())
	}
}

func TestSeedingTorrentRestartsWithoutAQueueSlot(t *testing.T) {
	sw := newSwarm(t, 0, 1<<20)
	s, err := NewSession(Config{Ratio: func() float64 { return 1 }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sw.info.Name), sw.data, 0o644); err != nil {
		t.Fatal(err)
	}
	tor, err := s.Add(State{ID: "t1", Magnet: sw.magnet(), Info: sw.infoBytes(t), Dir: dir,
		Status: StatusSeeding, Finished: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	tor.Start()
	if tor.Running() {
		t.Fatal("a seeding torrent checking its data holds a queue slot")
	}
	waitFor(t, "the restarted torrent to seed", 10*time.Second, func() bool { return tor.Status() == StatusSeeding })
}
