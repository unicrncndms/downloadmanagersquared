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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	atorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/storage"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/rate"
)

// swarm is a set of local seeders for one file, each standing in for a peer on the
// internet at a TEST-NET address that nothing can reach directly.
type swarm struct {
	info    metainfo.Info
	hash    metainfo.Hash
	data    []byte
	addrs   map[string]string // public (TEST-NET) address -> real loopback address
	clients []*atorrent.Client
	pexSeen atomic.Bool // a peer offered PEX in its extended handshake
}

func newSwarm(t *testing.T, seeders int, size int) *swarm {
	t.Helper()
	src := t.TempDir()
	data := make([]byte, size)
	rand.Read(data)
	if err := os.WriteFile(filepath.Join(src, "file.bin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	s := newSwarmOf(t, seeders, src, "file.bin", false)
	s.data = data
	return s
}

// newPrivateSwarm is newSwarm for a torrent flagged private.
func newPrivateSwarm(t *testing.T, seeders int, size int) *swarm {
	t.Helper()
	src := t.TempDir()
	data := make([]byte, size)
	rand.Read(data)
	if err := os.WriteFile(filepath.Join(src, "file.bin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	s := newSwarmOf(t, seeders, src, "file.bin", true)
	s.data = data
	return s
}

// newMultiSwarm seeds a torrent named "pack" holding one file of each size,
// f0.bin, f1.bin and so on.
func newMultiSwarm(t *testing.T, seeders int, sizes ...int) *swarm {
	t.Helper()
	src := t.TempDir()
	os.Mkdir(filepath.Join(src, "pack"), 0o755)
	for i, n := range sizes {
		data := make([]byte, n)
		rand.Read(data)
		if err := os.WriteFile(filepath.Join(src, "pack", fmt.Sprintf("f%d.bin", i)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return newSwarmOf(t, seeders, src, "pack", false)
}

// newSwarmOf seeds src/name, a file or a folder.
func newSwarmOf(t *testing.T, seeders int, src, name string, private bool) *swarm {
	t.Helper()
	info := metainfo.Info{PieceLength: 256 << 10}
	if err := info.BuildFromFilePath(filepath.Join(src, name)); err != nil {
		t.Fatal(err)
	}
	if private {
		info.Private = &private
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	mi := metainfo.MetaInfo{InfoBytes: infoBytes}
	s := &swarm{info: info, hash: mi.HashInfoBytes(), addrs: map[string]string{}}
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
		cfg.Callbacks.ReadExtendedHandshake = func(_ *atorrent.PeerConn, m *pp.ExtendedHandshakeMessage) {
			if _, ok := m.M[pp.ExtensionNamePex]; ok {
				s.pexSeen.Store(true)
			}
		}
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
		ID:     id,
		Upload: true,
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
		ListenPacket: func(port int) (net.PacketConn, error) {
			return net.ListenPacket("udp4", fmt.Sprintf("127.0.0.1:%d", port))
		},
		LookupIP: func(context.Context, string) ([]net.IP, error) {
			return nil, errors.New("no DNS in tests")
		},
	}
}

// inSwarm puts a torrent in the swarm, so the session opens its sockets.
func inSwarm(t *testing.T, s *Session, sw *swarm) *Torrent {
	t.Helper()
	tor, err := s.Add(State{ID: "joined", Magnet: sw.magnet(), Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	tor.Start()
	waitFor(t, "the session to go live", 5*time.Second, s.Live)
	return tor
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

func TestMagnetFetchesItsFilesThenWaitsForTheChoice(t *testing.T) {
	sw := newMultiSwarm(t, 1, 256<<10, 512<<10)
	var peers atomic.Int32
	s, err := NewSession(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetIfaces([]Iface{sw.iface("eth", &peers)})
	dir := t.TempDir()
	tor, err := s.Add(State{ID: "t1", Magnet: sw.magnet(), Dir: dir, Choose: true})
	if err != nil {
		t.Fatal(err)
	}
	tor.Start()
	if tor.Running() {
		t.Fatal("fetching metadata holds a queue slot")
	}
	waitFor(t, "the torrent's files", 10*time.Second, func() bool { return tor.Status() == StatusChoosing })
	files := tor.Summary().Files
	if len(files) != 2 || files[0].Path != "f0.bin" || files[1].Size != 512<<10 || !files[0].Selected || !files[1].Selected {
		t.Fatalf("files offered = %+v, want f0.bin and f1.bin, both ticked", files)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("waiting for the choice, the torrent already wrote %d entries", len(entries))
	}
}

func TestOnlyTickedFilesDownloadIntoTheChosenFolder(t *testing.T) {
	// Files on piece boundaries, so no piece holds part of both.
	sw := newMultiSwarm(t, 1, 512<<10, 512<<10)
	var peers atomic.Int32
	s, err := NewSession(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetIfaces([]Iface{sw.iface("eth", &peers)})
	tor, err := s.Add(State{ID: "t1", Magnet: sw.magnet(), Dir: t.TempDir(), Choose: true})
	if err != nil {
		t.Fatal(err)
	}
	tor.Start()
	waitFor(t, "the torrent's files", 10*time.Second, func() bool { return tor.Status() == StatusChoosing })

	chosen := t.TempDir()
	if err := tor.Choose([]bool{false, true}, chosen); err != nil {
		t.Fatal(err)
	}
	if tor.Status() != download.StatusQueued {
		t.Fatalf("after the choice the torrent is %q, want queued", tor.Status())
	}
	if sum := tor.Summary(); sum.Size != 512<<10 {
		t.Fatalf("size = %d, want only the ticked file's %d", sum.Size, 512<<10)
	}
	tor.Start()
	waitFor(t, "the ticked file", 30*time.Second, func() bool { return tor.Status() == download.StatusCompleted })
	// A multi-file torrent saves into a folder named after it.
	if _, err := os.Stat(filepath.Join(chosen, "pack", "f1.bin")); err != nil {
		t.Fatalf("ticked file missing: %v", err)
	}
	for _, p := range []string{"f0.bin", "f0.bin.part"} {
		if _, err := os.Stat(filepath.Join(chosen, "pack", p)); err == nil {
			t.Fatalf("unticked file was downloaded (%s exists)", p)
		}
	}
}

func TestChoosingNoFileIsRefused(t *testing.T) {
	sw := newMultiSwarm(t, 0, 256<<10, 256<<10)
	s, err := NewSession(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tor, err := s.Add(State{ID: "t1", Magnet: sw.magnet(), Info: sw.infoBytes(t), Dir: t.TempDir(), Choose: true})
	if err != nil {
		t.Fatal(err)
	}
	tor.Start() // the files are known: straight to the choice
	if tor.Status() != StatusChoosing {
		t.Fatalf("a torrent with known files is %q, want choosing", tor.Status())
	}
	if err := tor.Choose([]bool{false, false}, ""); err == nil {
		t.Fatal("choosing no file was accepted")
	}
}

func TestInterfaceWithUploadOffNeverUploads(t *testing.T) {
	sw := newSwarm(t, 0, 1<<20)
	leech := sw.leecher(t, "192.0.2.50:6881")
	var peers atomic.Int32
	s, err := NewSession(Config{Ratio: func() float64 { return 1 }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	metered := sw.iface("lte", &peers)
	metered.Upload = false
	s.SetIfaces([]Iface{metered})

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sw.info.Name), sw.data, 0o644); err != nil {
		t.Fatal(err)
	}
	tor, err := s.Add(State{ID: "t1", Magnet: sw.magnet(), Info: sw.infoBytes(t), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	tor.Start()
	waitFor(t, "the torrent to seed", 10*time.Second, func() bool { return tor.Status() == StatusSeeding })
	waitFor(t, "the leecher to connect", 10*time.Second, func() bool { return peers.Load() > 0 })
	time.Sleep(3 * time.Second)
	if n := leech.BytesCompleted(); n != 0 || tor.Summary().Uploaded != 0 {
		t.Fatalf("with upload off the leecher got %d bytes (uploaded %d)", n, tor.Summary().Uploaded)
	}

	metered.Upload = true
	s.SetIfaces([]Iface{metered})
	waitFor(t, "the leecher to complete once upload is on", 30*time.Second, func() bool {
		return leech.BytesCompleted() == int64(len(sw.data))
	})
}

func TestPeerConnectsInThroughTheInterfacesListener(t *testing.T) {
	sw := newSwarm(t, 0, 1<<20)
	leech := sw.leecher(t, "192.0.2.50:6881")
	s, err := NewSession(Config{Ratio: func() float64 { return 1 }, Port: freePort(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var dialed atomic.Int32
	ifc := sw.iface("eth", &dialed)
	ifc.Dial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("outgoing blocked") }
	var accepted atomic.Int32
	ifc.Listen = func(port int) (net.Listener, error) {
		ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return nil, err
		}
		return countingListener{ln, &accepted}, nil
	}
	s.SetIfaces([]Iface{ifc})

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sw.info.Name), sw.data, 0o644); err != nil {
		t.Fatal(err)
	}
	tor, err := s.Add(State{ID: "t1", Magnet: sw.magnet(), Info: sw.infoBytes(t), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	tor.Start()
	waitFor(t, "the torrent to seed", 10*time.Second, func() bool { return tor.Status() == StatusSeeding })
	leech.AddPeers([]atorrent.PeerInfo{{Addr: ipPortAddr{net.IPv4(127, 0, 0, 1), s.Port()}}})
	waitFor(t, "the incoming leecher to complete", 30*time.Second, func() bool {
		return leech.BytesCompleted() == int64(len(sw.data))
	})
	if accepted.Load() == 0 {
		t.Fatal("no peer came in through the interface's listener")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

type countingListener struct {
	net.Listener
	n *atomic.Int32
}

func (l countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.n.Add(1)
	}
	return c, err
}

type ipPortAddr struct {
	ip   net.IP
	port int
}

func (a ipPortAddr) String() string { return net.JoinHostPort(a.ip.String(), strconv.Itoa(a.port)) }

func TestTorrentPausedForTheVPNSaysSoAndResumes(t *testing.T) {
	sw := newSwarm(t, 1, 256<<10)
	s, err := NewSession(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tor, err := s.Add(State{ID: "t1", Magnet: sw.magnet(), Dir: t.TempDir(), VPN: paths.VPNOnly})
	if err != nil {
		t.Fatal(err)
	}
	tor.Start()
	tor.PauseForVPN()
	if !tor.WaitingForVPN() || tor.Status() != download.StatusPaused || tor.Active() {
		t.Fatalf("paused for the VPN: waiting %v, status %q, active %v", tor.WaitingForVPN(), tor.Status(), tor.Active())
	}
	if !strings.Contains(tor.Summary().Error, "VPN") {
		t.Fatalf("paused with %q", tor.Summary().Error)
	}
	tor.Stop(download.StatusQueued)
	tor.Start()
	if tor.WaitingForVPN() || !tor.Active() {
		t.Fatal("restarted torrent still waits for the VPN")
	}
	if tor.State().VPN != paths.VPNOnly {
		t.Fatal("the torrent's VPN mode was lost")
	}
}

func TestInterfaceOfANewGenerationGetsNewSockets(t *testing.T) {
	sw := newSwarm(t, 0, 256<<10)
	s, err := NewSession(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var opened atomic.Int32
	var peers atomic.Int32
	ifc := sw.iface("lo~vpn", &peers)
	listen := ifc.ListenPacket
	ifc.ListenPacket = func(port int) (net.PacketConn, error) { opened.Add(1); return listen(port) }
	s.SetIfaces([]Iface{ifc})
	inSwarm(t, s, sw)
	s.SetIfaces([]Iface{ifc})
	if opened.Load() != 1 {
		t.Fatalf("an unchanged interface opened %d sockets", opened.Load())
	}
	ifc.Gen = 1 // the tunnel was restarted: its old sockets are on a dead stack
	s.SetIfaces([]Iface{ifc})
	if opened.Load() != 2 {
		t.Fatal("a restarted tunnel kept its old sockets")
	}
}

// trackedPC is a socket whose closing is counted.
type trackedPC struct {
	net.PacketConn
	open *atomic.Int32
	once sync.Once
}

func (p *trackedPC) Close() error {
	p.once.Do(func() { p.open.Add(-1) })
	return p.PacketConn.Close()
}

func TestSessionOpensNothingUntilATorrentJoinsTheSwarm(t *testing.T) {
	sw := newSwarm(t, 0, 256<<10)
	s, err := NewSession(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var peers, open atomic.Int32
	ifc := sw.iface("wlan", &peers)
	listen := ifc.ListenPacket
	ifc.ListenPacket = func(port int) (net.PacketConn, error) {
		pc, err := listen(port)
		if err != nil {
			return nil, err
		}
		open.Add(1)
		return &trackedPC{PacketConn: pc, open: &open}, nil
	}
	s.SetIfaces([]Iface{ifc})
	tor, err := s.Add(State{ID: "t1", Magnet: sw.magnet(), Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if open.Load() != 0 || s.Live() {
		t.Fatalf("with no torrent in the swarm, %d sockets are open (live %v)", open.Load(), s.Live())
	}
	tor.Start()
	waitFor(t, "the session's sockets", 5*time.Second, func() bool { return open.Load() > 0 })
	if !s.Live() {
		t.Fatal("a session with a torrent in the swarm isn't live")
	}
	tor.Stop(download.StatusPaused)
	if open.Load() != 0 || s.Live() {
		t.Fatalf("after the last torrent left, %d sockets are open (live %v)", open.Load(), s.Live())
	}
	tor.Stop(download.StatusQueued)
	tor.Start()
	waitFor(t, "the sockets to open again", 5*time.Second, func() bool { return open.Load() > 0 })
}

func TestDownloadingTorrentShowsPeersAndSpeedPerInterface(t *testing.T) {
	sw := newSwarm(t, 2, 4<<20)
	s, err := NewSession(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var a, b atomic.Int32
	// Slow enough to watch: 512 KB/s on each interface.
	slow := func(ifc Iface) Iface {
		sh := rate.NewShaper()
		sh.SetLimits(0, 512<<10)
		dial := ifc.Dial
		ifc.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := dial(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return sh.Conn(c), nil
		}
		return ifc
	}
	s.SetIfaces([]Iface{slow(sw.iface("wlan", &a)), slow(sw.iface("eth", &b))})
	tor, err := s.Add(State{ID: "t1", Magnet: sw.magnet(), Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	tor.Start()
	defer tor.Stop(download.StatusPaused)
	waitFor(t, "peers and speeds on both interfaces", 15*time.Second, func() bool {
		sum := tor.Summary()
		w, e := sum.Links["wlan"], sum.Links["eth"]
		return sum.Status == download.StatusRunning && w.Conns > 0 && e.Conns > 0 && w.Speed > 0 && e.Speed > 0 && sum.Seeds > 0 && sum.Peers >= 2
	})
	if sum := tor.Summary(); sum.InfoHash != sw.hash.HexString() {
		t.Fatalf("info hash shown as %q", sum.InfoHash)
	}
}

func TestFinishedTorrentResumesToSeedWithoutASlot(t *testing.T) {
	s, err := NewSession(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	done, err := s.Add(State{ID: "f1", Magnet: newSwarm(t, 0, 256<<10).magnet(), Dir: t.TempDir(),
		Status: download.StatusPaused, Finished: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	half, err := s.Add(State{ID: "f2", Magnet: fmt.Sprintf("magnet:?xt=urn:btih:%040x", 5), Dir: t.TempDir(),
		Status: download.StatusPaused})
	if err != nil {
		t.Fatal(err)
	}
	done.Resume()
	half.Resume()
	if done.Status() != StatusSeeding || !done.Rejoins() {
		t.Fatalf("a finished torrent resumed as %q, rejoining %v; want seeding without a slot", done.Status(), done.Rejoins())
	}
	if half.Status() != download.StatusQueued || half.Rejoins() || !half.Yields() {
		t.Fatalf("an unfinished torrent resumed as %q, rejoining %v", half.Status(), half.Rejoins())
	}
}
