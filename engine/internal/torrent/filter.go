package torrent

import (
	"encoding/binary"
	"errors"
	"net"
	"sync"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

const (
	plainProtocol = "\x13BitTorrent protocol"
	handshakeLen  = len(plainProtocol) + 8 + 20 + 20 // protocol, reserved bits, info hash, peer id
)

var errEncrypted = errors.New("an encrypted peer connection can't be kept from uploading")

var (
	chokeMsg   = []byte{0, 0, 0, 1, byte(pp.Choke)}
	unchokeMsg = []byte{0, 0, 0, 1, byte(pp.Unchoke)}
)

// uploadGate keeps a peer from downloading from us while its interface has
// upload off. The client can't switch uploading off per peer, so the gate
// drops what would let the peer ask for data (unchoke, allowed fast) and any
// piece data from what we send, and remembers whether the client meant the
// peer unchoked, so switching upload sends the choke or unchoke it missed.
//
// That needs a plain stream. An encrypted handshake fails while upload is off
// (the client then retries the peer without encryption); while upload is on it
// passes untouched, and switching upload off closes it.
type uploadGate struct {
	net.Conn

	mu        sync.Mutex
	upload    bool
	encrypted bool
	err       error
	hs        int     // handshake bytes seen
	held      []byte  // the handshake's start, until it shows whether the stream is plain
	hdr       [5]byte // the current message's length prefix and type
	nhdr      int
	body      uint32 // the current message's bytes still to come
	dropMsg   bool
	unchoked  bool // the client unchoked the peer last, whether or not it was sent
}

func newUploadGate(c net.Conn, upload bool) *uploadGate {
	return &uploadGate{Conn: c, upload: upload}
}

// SetUpload switches uploading to this peer.
func (g *uploadGate) SetUpload(on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil || on == g.upload {
		return
	}
	g.upload = on
	if g.encrypted && !on {
		g.err = errEncrypted
		g.Conn.Close()
		return
	}
	if g.atBoundary() && g.unchoked {
		g.send(g.correction())
	}
}

// atBoundary is whether the next byte written starts a message.
func (g *uploadGate) atBoundary() bool {
	return g.hs == handshakeLen && g.nhdr == 0 && g.body == 0
}

// correction is the message the peer missed, given that the client unchoked it.
func (g *uploadGate) correction() []byte {
	if g.upload {
		return unchokeMsg
	}
	return chokeMsg
}

func (g *uploadGate) send(b []byte) {
	if _, err := g.Conn.Write(b); err != nil {
		g.err = err
	}
}

func (g *uploadGate) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return 0, g.err
	}
	if g.encrypted {
		return g.Conn.Write(p)
	}
	out := make([]byte, 0, len(p))
	for b := p; len(b) > 0; {
		switch {
		case g.hs < len(plainProtocol):
			n := min(len(b), len(plainProtocol)-g.hs)
			g.held = append(g.held, b[:n]...)
			g.hs += n
			b = b[n:]
			if string(g.held) != plainProtocol[:len(g.held)] {
				if !g.upload {
					g.err = errEncrypted
					return 0, g.err
				}
				g.encrypted = true
				out = append(append(out, g.held...), b...)
				b = nil
				g.held = nil
			} else if g.hs == len(plainProtocol) {
				out = append(out, g.held...)
				g.held = nil
			}
		case g.hs < handshakeLen:
			n := min(len(b), handshakeLen-g.hs)
			out = append(out, b[:n]...)
			g.hs += n
			b = b[n:]
		case g.body > 0:
			n := min(uint32(len(b)), g.body)
			if !g.dropMsg {
				out = append(out, b[:n]...)
			}
			g.body -= n
			b = b[n:]
		default:
			g.hdr[g.nhdr] = b[0]
			g.nhdr++
			b = b[1:]
			if g.nhdr == 4 && binary.BigEndian.Uint32(g.hdr[:4]) == 0 { // keepalive
				out = append(out, g.hdr[:4]...)
				g.nhdr = 0
			} else if g.nhdr == 5 {
				g.message(pp.MessageType(g.hdr[4]))
				if !g.dropMsg {
					out = append(out, g.hdr[:]...)
				}
				g.body = binary.BigEndian.Uint32(g.hdr[:4]) - 1
				g.nhdr = 0
			}
		}
	}
	if len(out) > 0 {
		if _, err := g.Conn.Write(out); err != nil {
			g.err = err
			return 0, err
		}
	}
	return len(p), nil
}

// message notes a message the client sends and decides whether it reaches the peer.
func (g *uploadGate) message(t pp.MessageType) {
	switch t {
	case pp.Choke:
		g.unchoked = false
	case pp.Unchoke:
		g.unchoked = true
	}
	g.dropMsg = !g.upload && (t == pp.Unchoke || t == pp.Piece || t == pp.AllowedFast)
}
