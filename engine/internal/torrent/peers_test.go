package torrent

import "testing"

// The peer limit is the setting for each line, which is a peer of its own, and follows it.
func TestThePeerLimitIsPerInterfaceAndFollowsTheSetting(t *testing.T) {
	per := 100
	s, err := NewSession(Config{PeersPerLine: func() int { return per }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n := s.maxPeers(); n != 100 {
		t.Fatalf("limit %d, want 100", n)
	}
	per = 30
	if n := s.maxPeers(); n != 30 {
		t.Fatalf("after the setting changed: %d, want 30", n)
	}
}
