package torrent

import (
	"bytes"
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrivateTorrentOffersNoPEX(t *testing.T) {
	for _, private := range []bool{false, true} {
		var sw *swarm
		if private {
			sw = newPrivateSwarm(t, 1, 256<<10)
		} else {
			sw = newSwarm(t, 1, 256<<10)
		}
		var peers atomic.Int32
		s, err := NewSession(Config{})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		s.SetIfaces([]Iface{sw.iface("eth", &peers)})
		tor, err := s.Add(State{ID: "t1", Magnet: sw.magnet(), Info: sw.infoBytes(t), Dir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		tor.Start()
		waitFor(t, "the download", 30*time.Second, func() bool { return tor.Status() == StatusSeeding || tor.Status() == "completed" })
		if sw.pexSeen.Load() == private {
			t.Fatalf("private %v: PEX offered to the peer = %v", private, sw.pexSeen.Load())
		}
	}
}

// dhtNode is a DHT bootstrap node that only records what it is asked.
type dhtNode struct {
	pc       net.PacketConn
	received atomic.Value // [][]byte
}

func newDHTNode(t *testing.T) *dhtNode {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	n := &dhtNode{pc: pc}
	n.received.Store([][]byte{})
	go func() {
		buf := make([]byte, 2048)
		for {
			k, _, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			n.received.Store(append(n.received.Load().([][]byte), bytes.Clone(buf[:k])))
		}
	}()
	return n
}

// asked is whether any query mentioned the info hash.
func (n *dhtNode) asked(hash []byte) bool {
	for _, p := range n.received.Load().([][]byte) {
		if bytes.Contains(p, hash) {
			return true
		}
	}
	return false
}

func TestPrivateTorrentStaysOffTheDHT(t *testing.T) {
	for _, private := range []bool{false, true} {
		var sw *swarm
		if private {
			sw = newPrivateSwarm(t, 0, 256<<10)
		} else {
			sw = newSwarm(t, 0, 256<<10)
		}
		node := newDHTNode(t)
		_, port, _ := net.SplitHostPort(node.pc.LocalAddr().String())
		s, err := NewSession(Config{DHTBootstrap: []string{"dht.test:" + port}})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		var peers atomic.Int32
		ifc := sw.iface("eth", &peers)
		ifc.LookupIP = func(context.Context, string) ([]net.IP, error) { return []net.IP{net.IPv4(127, 0, 0, 1)}, nil }
		s.SetIfaces([]Iface{ifc})
		tor, err := s.Add(State{ID: "t1", Magnet: sw.magnet(), Info: sw.infoBytes(t), Dir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		tor.Start()
		if !private {
			waitFor(t, "the public torrent's DHT lookup", 10*time.Second, func() bool { return node.asked(sw.hash[:]) })
			continue
		}
		time.Sleep(5 * time.Second)
		if node.asked(sw.hash[:]) {
			t.Fatal("the private torrent was looked up on the DHT")
		}
	}
}
