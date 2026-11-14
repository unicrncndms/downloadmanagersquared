package netif

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"time"
)

// STUN (RFC 5389): one binding request tells the public address a socket
// reaches the internet with. Only sent when the user asks (Check lines).

const (
	stunMagic       = 0x2112A442
	stunBindRequest = 0x0001
	stunBindSuccess = 0x0101
	attrMapped      = 0x0001
	attrXorMapped   = 0x0020
)

// StunServer is asked by Check lines; a public server any app may use.
const StunServer = "stun.cloudflare.com:3478"

func stunRequest() (msg []byte, txid [12]byte) {
	rand.Read(txid[:])
	msg = make([]byte, 20)
	binary.BigEndian.PutUint16(msg[0:], stunBindRequest)
	binary.BigEndian.PutUint32(msg[4:], stunMagic)
	copy(msg[8:], txid[:])
	return msg, txid
}

// parseStunResponse is the address, IPv4 or IPv6, a binding response maps to.
func parseStunResponse(b []byte, txid [12]byte) (net.IP, error) {
	if len(b) < 20 || binary.BigEndian.Uint16(b[0:]) != stunBindSuccess ||
		binary.BigEndian.Uint32(b[4:]) != stunMagic || [12]byte(b[8:20]) != txid {
		return nil, errors.New("not an answer to this STUN request")
	}
	n := int(binary.BigEndian.Uint16(b[2:]))
	if 20+n > len(b) {
		return nil, errors.New("short STUN answer")
	}
	var mapped net.IP
	for a := b[20 : 20+n]; len(a) >= 4; {
		typ, l := binary.BigEndian.Uint16(a[0:]), int(binary.BigEndian.Uint16(a[2:]))
		if 4+l > len(a) {
			break
		}
		v := a[4 : 4+l]
		n := 0 // address length, by the family: IPv4 or IPv6
		if l >= 2 {
			n = map[byte]int{0x01: 4, 0x02: 16}[v[1]]
		}
		if (typ == attrXorMapped || typ == attrMapped) && n > 0 && l >= 4+n {
			ip := net.IP(append([]byte(nil), v[4:4+n]...))
			if typ == attrXorMapped {
				// XORed with the magic cookie, then (IPv6) the transaction ID.
				var m [16]byte
				binary.BigEndian.PutUint32(m[:], stunMagic)
				copy(m[4:], txid[:])
				for i := range ip {
					ip[i] ^= m[i]
				}
				return ip, nil
			}
			mapped = ip
		}
		a = a[4+(l+3)&^3:] // attributes are padded to 4 bytes
	}
	if mapped != nil {
		return mapped, nil
	}
	return nil, errors.New("the STUN answer has no address")
}

// stunQuery asks server over pc, retrying until ctx ends.
func stunQuery(ctx context.Context, pc net.PacketConn, server net.Addr) (net.IP, error) {
	msg, txid := stunRequest()
	buf := make([]byte, 1500)
	for {
		if _, err := pc.WriteTo(msg, server); err != nil {
			return nil, err
		}
		wait := time.Now().Add(time.Second)
		if d, ok := ctx.Deadline(); ok && d.Before(wait) {
			wait = d
		}
		pc.SetReadDeadline(wait)
		for {
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				break // resend, or give up below
			}
			if ip, err := parseStunResponse(buf[:n], txid); err == nil {
				return ip, nil
			}
		}
		if ctx.Err() != nil {
			return nil, errors.New("no answer from the STUN server")
		}
	}
}

// PublicIP is the public address this interface reaches the internet with,
// as the STUN server at server ("host:port") sees it: over IPv4 when the
// interface has IPv4, else over IPv6.
func (b *Binding) PublicIP(ctx context.Context, server string) (net.IP, error) {
	host, port, err := net.SplitHostPort(server)
	if err != nil {
		return nil, err
	}
	family, network, listen := "IPv4", "udp4", b.ListenPacket
	if !b.Has4() {
		family, network, listen = "IPv6", "udp6", b.ListenPacket6
	}
	ips, err := b.lookup(ctx, host, family)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("no " + family + " address for " + host)
	}
	addr, err := net.ResolveUDPAddr(network, net.JoinHostPort(ips[0].String(), port))
	if err != nil {
		return nil, err
	}
	pc, err := listen(0)
	if err != nil {
		return nil, err
	}
	defer pc.Close()
	return stunQuery(ctx, pc, addr)
}
