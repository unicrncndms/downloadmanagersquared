package rate

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// serve accepts connections that each send size bytes, or with size 0 read
// until the client closes.
func serve(t *testing.T, size int) string {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				if size == 0 {
					io.Copy(io.Discard, c)
					return
				}
				c.Write(make([]byte, size))
			}()
		}
	}()
	return ln.Addr().String()
}

// both runs f on two connections through s at once and returns how long it took.
func both(t *testing.T, s *Shaper, addr string, f func(net.Conn)) time.Duration {
	t.Helper()
	start := time.Now()
	var wg sync.WaitGroup
	for range 2 {
		c, err := net.Dial("tcp4", addr)
		if err != nil {
			t.Fatal(err)
		}
		c = s.Conn(c)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer c.Close()
			f(c)
		}()
	}
	wg.Wait()
	return time.Since(start)
}

func TestDownloadLimitHoldsAcrossConnections(t *testing.T) {
	const size = 200 << 10
	addr := serve(t, size)
	s := NewShaper()
	s.SetLimits(0, 200<<10) // two connections of 200 KB at 200 KB/s together: about 2 s
	took := both(t, s, addr, func(c net.Conn) {
		if n, _ := io.Copy(io.Discard, c); n != size {
			t.Errorf("read %d bytes, want %d", n, size)
		}
	})
	if took < 1500*time.Millisecond {
		t.Fatalf("400 KB at 200 KB/s took %v", took)
	}
}

func TestUploadLimitHoldsAcrossConnections(t *testing.T) {
	addr := serve(t, 0)
	s := NewShaper()
	s.SetLimits(200<<10, 0)
	took := both(t, s, addr, func(c net.Conn) {
		if _, err := c.Write(make([]byte, 200<<10)); err != nil {
			t.Error(err)
		}
	})
	if took < 1500*time.Millisecond {
		t.Fatalf("400 KB at 200 KB/s took %v", took)
	}
}

func TestNoLimitIsFast(t *testing.T) {
	const size = 4 << 20
	addr := serve(t, size)
	s := NewShaper()
	took := both(t, s, addr, func(c net.Conn) { io.Copy(io.Discard, c) })
	if took > time.Second {
		t.Fatalf("8 MB over loopback with no limit took %v", took)
	}
}

func TestPacketsCountAgainstTheSameLimit(t *testing.T) {
	a, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	s := NewShaper()
	s.SetLimits(100<<10, 0)
	pc := s.PacketConn(a)
	start := time.Now()
	buf := make([]byte, 1000)
	for range 200 { // 200 KB at 100 KB/s
		if _, err := pc.WriteTo(buf, a.LocalAddr()); err != nil {
			t.Fatal(err)
		}
	}
	if took := time.Since(start); took < 1500*time.Millisecond {
		t.Fatalf("200 KB of packets at 100 KB/s took %v", took)
	}
}
