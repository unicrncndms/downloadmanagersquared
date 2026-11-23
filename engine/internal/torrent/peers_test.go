package torrent

import "testing"

// The peer limit is the setting for each interface in use, and follows both.
func TestThePeerLimitIsPerInterfaceAndFollowsTheSetting(t *testing.T) {
	per := 100
	s, err := NewSession(Config{PeersPerLine: func() int { return per }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n := s.maxPeers(); n != 100 {
		t.Fatalf("no interface yet: %d, want 100", n)
	}
	s.mu.Lock()
	s.ifs = []*ifaceSockets{{}, {}}
	s.mu.Unlock()
	if n := s.maxPeers(); n != 200 {
		t.Fatalf("two interfaces: %d, want 200", n)
	}
	per = 30
	if n := s.maxPeers(); n != 60 {
		t.Fatalf("after the setting changed: %d, want 60", n)
	}
}
