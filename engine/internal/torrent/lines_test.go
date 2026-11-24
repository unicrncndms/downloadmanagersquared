package torrent

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/rate"
)

// slowed is ifc with its downloads held to bps, so a torrent takes long enough to watch.
func slowed(ifc Iface, bps int64) Iface {
	sh := rate.NewShaper()
	sh.SetLimits(0, bps)
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

// linesSession is a session over the lines wlan and eth, each 512 KB/s, and
// the counts of the peers each carried.
func linesSession(t *testing.T, sw *swarm, cfg Config) (s *Session, lines []Iface, wlan, eth *atomic.Int32) {
	t.Helper()
	s, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	wlan, eth = new(atomic.Int32), new(atomic.Int32)
	lines = []Iface{slowed(sw.iface("wlan", wlan), 512<<10), slowed(sw.iface("eth", eth), 512<<10)}
	s.SetIfaces(lines)
	return s, lines, wlan, eth
}

func fileIs(t *testing.T, tor *Torrent, sw *swarm) {
	t.Helper()
	// The status flips when the last piece is verified; the file is promoted from .part a moment later.
	waitFor(t, "the file to be complete", 10*time.Second, func() bool { _, err := os.Stat(tor.Path()); return err == nil })
	got, err := os.ReadFile(tor.Path())
	if err != nil || !bytes.Equal(got, sw.data) {
		t.Fatalf("the file: %d bytes, %v", len(got), err)
	}
}

// Each line is a peer of its own: the remote holds a connection from each, with
// its own peer ID, and both lines fetch pieces. The line that wasn't first
// got its info dict from the first, not from the remote.
func TestTwoLinesAreTwoPeersOfOneRemoteAndBothGetPieces(t *testing.T) {
	sw := newSwarm(t, 1, 4<<20)
	s, _, wlan, eth := linesSession(t, sw, Config{Ratio: func() float64 { return 100 }})
	tor, err := s.Add(State{ID: "t1", Magnet: sw.magnet(), Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	tor.Start()
	defer tor.Stop(download.StatusPaused)
	waitFor(t, "two connections at the remote, with different peer IDs", 20*time.Second, func() bool {
		conns := sw.clients[0].Torrents()[0].PeerConns()
		return len(conns) == 2 && conns[0].PeerID != conns[1].PeerID
	})
	waitFor(t, "the torrent to seed", 30*time.Second, func() bool { return tor.Status() == StatusSeeding })
	fileIs(t, tor, sw)
	if wlan.Load() == 0 || eth.Load() == 0 {
		t.Fatalf("peers carried: wlan %d, eth %d", wlan.Load(), eth.Load())
	}
	lts := tor.lineTors()
	if len(lts) != 2 {
		t.Fatalf("%d lines carry the torrent", len(lts))
	}
	for _, lt := range lts {
		st := lt.at.Stats()
		if st.BytesReadUsefulData.Int64() == 0 {
			t.Errorf("line %s fetched no piece", lt.is.ID)
		}
		if lt.at.Info() == nil || lt.at.BytesCompleted() != int64(len(sw.data)) {
			t.Errorf("line %s sees %d of %d bytes done", lt.is.ID, lt.at.BytesCompleted(), len(sw.data))
		}
	}
	if lts[0].is.cl.PeerID() == lts[1].is.cl.PeerID() {
		t.Error("the lines share a peer ID")
	}
}

// No piece is fetched by two lines: what the remote sent in all is the torrent,
// not a byte more.
func TestNoPieceIsDownloadedTwice(t *testing.T) {
	sw := newSwarm(t, 1, 8<<20)
	s, _, _, _ := linesSession(t, sw, Config{Ratio: func() float64 { return 100 }})
	tor, _ := s.Add(State{ID: "t1", Magnet: sw.magnet(), Dir: t.TempDir()})
	tor.Start()
	defer tor.Stop(download.StatusPaused)
	waitFor(t, "the torrent to seed", 40*time.Second, func() bool { return tor.Status() == StatusSeeding })
	seederStats := sw.clients[0].Torrents()[0].Stats()
	sent := seederStats.BytesWrittenData.Int64()
	if extra := sent - int64(len(sw.data)); extra > 0 {
		t.Fatalf("the remote sent %d bytes for a %d byte torrent: %d too many", sent, len(sw.data), extra)
	}
}

// A line that goes away mid-download hands what it had not finished to the other.
func TestALineDyingMidDownloadHandsItsPiecesToTheOther(t *testing.T) {
	sw := newSwarm(t, 1, 4<<20)
	s, lines, _, eth := linesSession(t, sw, Config{})
	tor, _ := s.Add(State{ID: "t1", Magnet: sw.magnet(), Dir: t.TempDir()})
	tor.Start()
	defer tor.Stop(download.StatusPaused)
	waitFor(t, "eth to be fetching", 20*time.Second, func() bool {
		return tor.Summary().Links["eth"].Speed > 0 && tor.Summary().Done > 0
	})
	if tor.Summary().Done >= int64(len(sw.data))*3/4 {
		t.Fatal("too late to take a line away")
	}
	s.SetIfaces(lines[:1]) // the 5G drops out
	waitFor(t, "the torrent to finish over wlan alone", 40*time.Second, func() bool {
		return tor.Status() == download.StatusCompleted
	})
	fileIs(t, tor, sw)
	if eth.Load() == 0 {
		t.Fatal("eth never carried a peer")
	}
}

// A line joining mid-download takes pieces too, and when all lines are gone
// the torrent waits and finishes over the one that comes back.
func TestALineJoiningMidDownloadTakesAShare(t *testing.T) {
	sw := newSwarm(t, 1, 4<<20)
	s, err := NewSession(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var a, b atomic.Int32
	wlan, eth := slowed(sw.iface("wlan", &a), 512<<10), slowed(sw.iface("eth", &b), 512<<10)
	s.SetIfaces([]Iface{wlan})
	tor, _ := s.Add(State{ID: "t1", Magnet: sw.magnet(), Dir: t.TempDir()})
	tor.Start()
	defer tor.Stop(download.StatusPaused)
	waitFor(t, "wlan to be fetching", 20*time.Second, func() bool { return tor.Summary().Done > 256<<10 })
	s.SetIfaces([]Iface{wlan, eth})
	waitFor(t, "eth to fetch as well", 30*time.Second, func() bool {
		return tor.Summary().Links["eth"].Speed > 0 && tor.Summary().Links["wlan"].Speed > 0
	})
	waitFor(t, "the torrent to finish", 40*time.Second, func() bool { return tor.Status() == download.StatusCompleted })
	fileIs(t, tor, sw)
}

// Lines share one storage, so nothing is held open when the file is complete:
// it can be renamed while the torrent seeds, and the torrent's data stays whole.
func TestFinishedFileOfTwoLinesCanBeRenamedWhileSeeding(t *testing.T) {
	sw := newSwarm(t, 1, 2<<20)
	s, _, _, _ := linesSession(t, sw, Config{Ratio: func() float64 { return 100 }})
	tor, _ := s.Add(State{ID: "t1", Magnet: sw.magnet(), Dir: t.TempDir()})
	tor.Start()
	defer tor.Stop(download.StatusPaused)
	waitFor(t, "the torrent to seed", 30*time.Second, func() bool { return tor.Status() == StatusSeeding })
	fileIs(t, tor, sw)
	waitFor(t, "no .part file left behind", 40*time.Second, func() bool {
		_, err := os.Stat(tor.Path() + ".part")
		return err != nil
	})
	moved := filepath.Join(filepath.Dir(tor.Path()), "moved.bin")
	if err := os.Rename(tor.Path(), moved); err != nil {
		t.Fatalf("the finished file is held open: %v", err)
	}
}

// A private torrent runs on the main line only: its tracker may count each
// peer ID or IP of an account as another user.
func TestPrivateTorrentUsesOneLine(t *testing.T) {
	sw := newPrivateSwarm(t, 1, 1<<20)
	s, _, wlan, eth := linesSession(t, sw, Config{Ratio: func() float64 { return 100 }})
	tor, _ := s.Add(State{ID: "t1", Magnet: sw.magnet(), Dir: t.TempDir()})
	tor.Start()
	defer tor.Stop(download.StatusPaused)
	waitFor(t, "the torrent to seed", 30*time.Second, func() bool { return tor.Status() == StatusSeeding })
	time.Sleep(time.Second) // a second line would have joined by now
	if n := len(tor.lineTors()); n != 1 {
		t.Fatalf("%d lines carry a private torrent", n)
	}
	if eth.Load() != 0 || wlan.Load() == 0 {
		t.Fatalf("peers carried: wlan %d, eth %d; want all on the main line", wlan.Load(), eth.Load())
	}
	fileIs(t, tor, sw)
}

// The card is one torrent: its peers are those of the lines added up, each line
// shows its own share, and the done count is that of the torrent, not a line's.
func TestSummaryAddsUpTheLines(t *testing.T) {
	sw := newSwarm(t, 2, 4<<20)
	s, _, _, _ := linesSession(t, sw, Config{})
	tor, _ := s.Add(State{ID: "t1", Magnet: sw.magnet(), Dir: t.TempDir()})
	tor.Start()
	defer tor.Stop(download.StatusPaused)
	waitFor(t, "both lines to show peers and speed", 20*time.Second, func() bool {
		sum := tor.Summary()
		return sum.Status == download.StatusRunning && sum.Links["wlan"].Speed > 0 && sum.Links["eth"].Speed > 0
	})
	sum := tor.Summary()
	w, e := sum.Links["wlan"], sum.Links["eth"]
	if w.Conns == 0 || e.Conns == 0 || sum.Peers < w.Conns+e.Conns-1 || sum.Peers > w.Conns+e.Conns+1 {
		t.Fatalf("peers %d, wlan %d, eth %d", sum.Peers, w.Conns, e.Conns)
	}
	if sum.Seeds != sum.Peers {
		t.Fatalf("seeds %d of %d peers, all are seeders", sum.Seeds, sum.Peers)
	}
	// Each line says what its peers sent and what of it wasn't used.
	if w.Read <= 0 || e.Read <= 0 || w.Wasted < 0 || w.Wasted > w.Read || e.Wasted > e.Read {
		t.Fatalf("read and wasted: wlan %d/%d, eth %d/%d", w.Read, w.Wasted, e.Read, e.Wasted)
	}
	if sum.Speed <= 0 || sum.Done <= 0 || sum.Done > sum.Size {
		t.Fatalf("speed %v, done %d of %d", sum.Speed, sum.Done, sum.Size)
	}
	waitFor(t, "the torrent to finish", 40*time.Second, func() bool { return tor.Status() == download.StatusCompleted })
	if got := tor.Summary(); got.Done != got.Size || got.Size != int64(len(sw.data)) {
		t.Fatalf("done %d of %d", got.Done, got.Size)
	}
}

// What a leaving line uploaded stays in the torrent's count.
func TestUploadedKeepsWhatALeavingLineSent(t *testing.T) {
	sw := newSwarm(t, 0, 1<<20)
	leech := sw.leecher(t, "192.0.2.50:6881")
	s, err := NewSession(Config{Ratio: func() float64 { return 100 }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var a, b atomic.Int32
	wlan, eth := sw.iface("wlan", &a), sw.iface("eth", &b)
	s.SetIfaces([]Iface{wlan, eth})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sw.info.Name), sw.data, 0o644); err != nil {
		t.Fatal(err)
	}
	tor, err := s.Add(State{ID: "t1", Magnet: sw.magnet(), Info: sw.infoBytes(t), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	tor.Start()
	defer tor.Stop(download.StatusPaused)
	waitFor(t, "the torrent to seed on both lines", 20*time.Second, func() bool {
		return tor.Status() == StatusSeeding && len(tor.lineTors()) == 2
	})
	waitFor(t, "the leecher to take pieces from us", 30*time.Second, func() bool {
		return leech.BytesCompleted() == int64(len(sw.data))
	})
	waitFor(t, "the upload to be counted", 10*time.Second, func() bool { return tor.Summary().Uploaded >= int64(len(sw.data)) })
	before := tor.Summary().Uploaded
	s.SetIfaces([]Iface{wlan})
	waitFor(t, "the second line to leave", 10*time.Second, func() bool { return len(tor.lineTors()) == 1 })
	time.Sleep(time.Second)
	if after := tor.Summary().Uploaded; after < before {
		t.Fatalf("uploaded went from %d to %d when a line left", before, after)
	}
}

func TestAssignPiecesSplitsAsTheRatesSayAndKeepsWhatIsStarted(t *testing.T) {
	n := 100
	mk := func() shareIn {
		in := shareIn{owner: make([]int, n), todo: make([]bool, n), keep: make([]bool, n),
			avail: [][]bool{make([]bool, n), make([]bool, n)}, alive: []bool{true, true}, rate: []float64{1, 1}}
		for i := range n {
			in.owner[i], in.todo[i] = -1, true
			in.avail[0][i], in.avail[1][i] = true, true
		}
		return in
	}
	count := func(out []int) (c [2]int) {
		for _, l := range out {
			if l >= 0 {
				c[l]++
			}
		}
		return
	}
	if c := count(assignPieces(mk())); c != [2]int{50, 50} {
		t.Errorf("equal lines: %v, want 50 each", c)
	}
	in := mk()
	in.rate = []float64{3, 1}
	if c := count(assignPieces(in)); c[0] < 70 || c[0] > 80 {
		t.Errorf("a line three times as fast got %v", c)
	}
	// Pieces a line holds stay unless the others would finish them much sooner.
	in = mk()
	first := assignPieces(in)
	copy(in.owner, first)
	if second := assignPieces(in); count(second) != count(first) {
		t.Errorf("a settled split moved: %v then %v", count(first), count(second))
	}
	// A started piece stays with its line even when that line is slow.
	in = mk()
	in.rate = []float64{1, 100}
	in.owner[7], in.keep[7] = 0, true
	if out := assignPieces(in); out[7] != 0 {
		t.Errorf("a started piece moved to line %d", out[7])
	}
	// A dead line gets nothing it could leave to a living one; a piece only one line's peers have goes there.
	in = mk()
	in.alive[0] = false
	if c := count(assignPieces(in)); c[0] != 0 {
		t.Errorf("a line with no peers got %d pieces", c[0])
	}
	in = mk()
	for i := range 10 {
		in.avail[0][i] = false
	}
	out := assignPieces(in)
	for i := range 10 {
		if out[i] != 1 {
			t.Errorf("piece %d only line 1's peers have went to line %d", i, out[i])
		}
	}
	// A piece nothing has still goes to some line.
	in = mk()
	in.avail[0][3], in.avail[1][3] = false, false
	if out := assignPieces(in); out[3] < 0 {
		t.Error("a piece no peer has was left with no line")
	}
}

// A settled split stays put when the speeds change, as a piece moved after its
// line asked for it is fetched twice; only a line with nothing left takes pieces,
// from the end of the slowest line's share, and not one that is started.
func TestAssignPiecesMovesOnlyToALineThatRanOut(t *testing.T) {
	n := 100
	in := shareIn{owner: make([]int, n), todo: make([]bool, n), keep: make([]bool, n),
		avail: [][]bool{make([]bool, n), make([]bool, n)}, alive: []bool{true, true}, rate: []float64{1, 1}}
	for i := range n {
		in.owner[i], in.todo[i] = i%2, true
		in.avail[0][i], in.avail[1][i] = true, true
	}
	in.rate = []float64{10, 1} // line 0 is ten times as fast now, but still has pieces
	if out := assignPieces(in); !slices.Equal(out, in.owner) {
		t.Fatal("pieces moved between two lines that both still have some")
	}
	for i := range n { // line 0 finished its share
		in.todo[i] = i%2 == 1
	}
	in.keep[99] = true // started on line 1
	out := assignPieces(in)
	var took []int
	for i, l := range out {
		if in.todo[i] && l == 0 {
			took = append(took, i)
		}
	}
	if len(took) < 40 || len(took) > 46 {
		t.Fatalf("the idle line took %d of 50 pieces at ten times the speed, want about 45", len(took))
	}
	if out[99] != 1 {
		t.Fatal("a started piece moved")
	}
	for i := slices.Min(took); i < 99; i += 2 { // the slow line's pieces from there on, but the started one
		if out[i] != 0 {
			t.Fatalf("piece %d stayed on the slow line though the idle line took %d below it: not from the end", i, slices.Min(took))
		}
	}
}

// A torrent's time left is reckoned at its speed over the ETA window, as an HTTP
// download's is, not at the speed of the last half second.
func TestTimeLeftUsesTheETAWindow(t *testing.T) {
	sw := newSwarm(t, 1, 4<<20)
	var asked atomic.Bool
	s, err := NewSession(Config{ETAWindow: func() time.Duration { asked.Store(true); return time.Minute }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n atomic.Int32
	s.SetIfaces([]Iface{slowed(sw.iface("wlan", &n), 512<<10)})
	tor, _ := s.Add(State{ID: "t1", Magnet: sw.magnet(), Dir: t.TempDir()})
	tor.Start()
	defer tor.Stop(download.StatusPaused)
	waitFor(t, "a time left", 20*time.Second, func() bool { return tor.Summary().ETA > 0 })
	if eta := tor.Summary().ETA; eta > 30 || !asked.Load() {
		t.Fatalf("time left %v s for 4 MiB at about 512 KiB/s, window asked %v", eta, asked.Load())
	}
}

// Data already on disk is not speed: while the start-up check hashes it, what is
// done grows at the disk's pace, and the time left must not take that for peers'.
func TestTimeLeftIgnoresTheDataTheCheckFinds(t *testing.T) {
	sw := newSwarm(t, 1, 16<<20)
	dir := t.TempDir()
	half := make([]byte, len(sw.data)) // the first half fetched before, the rest not yet
	copy(half, sw.data[:len(sw.data)/2])
	if err := os.WriteFile(filepath.Join(dir, sw.info.Name+".part"), half, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewSession(Config{ETAWindow: func() time.Duration { return time.Minute }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n atomic.Int32
	s.SetIfaces([]Iface{slowed(sw.iface("wlan", &n), 256<<10)})
	tor, _ := s.Add(State{ID: "t1", Magnet: sw.magnet(), Dir: dir, Info: sw.infoBytes(t)})
	tor.Start()
	defer tor.Stop(download.StatusPaused)
	waitFor(t, "a time left", 30*time.Second, func() bool { return tor.Summary().ETA > 0 })
	sum := tor.Summary()
	if floor := float64(sum.Size-sum.Done) / (1 << 20); sum.ETA < floor { // 1 MiB/s is four times the line's speed
		t.Fatalf("time left %.1f s for %d bytes at about 256 KiB/s, want at least %.1f", sum.ETA, sum.Size-sum.Done, floor)
	}
}
