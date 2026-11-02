package torrent

import (
	"bytes"
	"crypto/rand"
	"net"
	"testing"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

// recorder is a peer connection that keeps everything written to it.
type recorder struct {
	net.Conn
	got    bytes.Buffer
	closed bool
}

func (r *recorder) Write(p []byte) (int, error) { return r.got.Write(p) }

func handshake() []byte {
	b := append([]byte("\x13BitTorrent protocol"), make([]byte, 8+20+20)...)
	return b
}

func msg(m pp.Message) []byte {
	b, err := m.MarshalBinary()
	if err != nil {
		panic(err)
	}
	return b
}

func TestUploadFilterSendsNothingThatLetsAPeerDownload(t *testing.T) {
	keep := [][]byte{
		handshake(),
		msg(pp.Message{Type: pp.Bitfield, Bitfield: []bool{true, false, true}}),
		msg(pp.Message{Type: pp.Interested}),
		msg(pp.Message{Type: pp.Request, Index: 1, Begin: 0, Length: 16384}),
		msg(pp.Message{Keepalive: true}),
		msg(pp.Message{Type: pp.Have, Index: 2}),
	}
	drop := [][]byte{
		msg(pp.Message{Type: pp.Unchoke}),
		msg(pp.Message{Type: pp.Piece, Index: 1, Begin: 0, Piece: bytes.Repeat([]byte{7}, 16384)}),
		msg(pp.Message{Type: pp.AllowedFast, Index: 3}),
	}
	var stream, want bytes.Buffer
	stream.Write(keep[0])
	want.Write(keep[0])
	for i := 1; i < len(keep); i++ {
		stream.Write(drop[(i-1)%len(drop)])
		stream.Write(keep[i])
		want.Write(keep[i])
	}
	// Whole, and split at every byte: messages straddle writes.
	for _, size := range []int{stream.Len(), 1, 5} {
		r := &recorder{}
		f := newUploadGate(r, false)
		for b := stream.Bytes(); len(b) > 0; {
			n := min(size, len(b))
			if w, err := f.Write(b[:n]); err != nil || w != n {
				t.Fatalf("write of %d bytes: %d, %v", n, w, err)
			}
			b = b[n:]
		}
		if !bytes.Equal(r.got.Bytes(), want.Bytes()) {
			t.Fatalf("writes of %d bytes: peer got %d bytes, want %d (no unchoke, allowed fast or piece)", size, r.got.Len(), want.Len())
		}
	}
}

func TestUploadFilterRefusesAnEncryptedHandshake(t *testing.T) {
	key := make([]byte, 96) // an MSE handshake starts with a random public key
	rand.Read(key)
	key[0] = 0x13 // even when it happens to start like the plain one
	r := &recorder{}
	if _, err := newUploadGate(r, false).Write(key); err == nil {
		t.Fatal("an encrypted stream, which can't be filtered, was let through")
	}
	if r.got.Len() != 0 {
		t.Fatalf("%d bytes of the encrypted handshake reached the peer", r.got.Len())
	}
}

func (r *recorder) Close() error { r.closed = true; return nil }

func TestSwitchingUploadOnSendsTheUnchokeThePeerMissed(t *testing.T) {
	r := &recorder{}
	g := newUploadGate(r, false)
	g.Write(handshake())
	g.Write(msg(pp.Message{Type: pp.Unchoke}))
	if r.got.Len() != len(handshake()) {
		t.Fatal("the unchoke reached the peer with upload off")
	}
	g.SetUpload(true)
	piece := msg(pp.Message{Type: pp.Piece, Index: 0, Piece: []byte("data")})
	g.Write(piece)
	want := append(append(handshake(), unchokeMsg...), piece...)
	if !bytes.Equal(r.got.Bytes(), want) {
		t.Fatalf("after switching upload on the peer got %x, want the unchoke and then the piece", r.got.Bytes()[len(handshake()):])
	}
}

func TestSwitchingUploadOffChokesThePeer(t *testing.T) {
	r := &recorder{}
	g := newUploadGate(r, true)
	g.Write(handshake())
	g.Write(msg(pp.Message{Type: pp.Unchoke}))
	g.SetUpload(false)
	g.Write(msg(pp.Message{Type: pp.Piece, Index: 0, Piece: []byte("data")}))
	want := append(append(handshake(), unchokeMsg...), chokeMsg...)
	if !bytes.Equal(r.got.Bytes(), want) {
		t.Fatalf("after switching upload off the peer got %x, want a choke and no piece", r.got.Bytes()[len(handshake()):])
	}
}

func TestEncryptedPeerIsClosedWhenUploadGoesOff(t *testing.T) {
	r := &recorder{}
	g := newUploadGate(r, true)
	key := make([]byte, 96)
	rand.Read(key)
	key[0] = 0x14
	if _, err := g.Write(key); err != nil || r.got.Len() != len(key) {
		t.Fatalf("with upload on an encrypted stream should pass: wrote %d, %v", r.got.Len(), err)
	}
	g.SetUpload(false)
	if !r.closed {
		t.Fatal("an encrypted peer that can't be filtered stayed open with upload off")
	}
}
