package netif

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Two adapters behind one router look like two lines but add no bandwidth.
// The gateway's MAC address tells them apart offline: two different routers
// are often both 192.168.1.1, but never the same MAC.

var (
	iphlpapi    = windows.NewLazySystemDLL("iphlpapi.dll")
	procSendARP = iphlpapi.NewProc("SendARP")
)

type macEntry struct {
	mac string
	at  time.Time
}

// The gateway MACs found so far, by "ip|gateway". An ARP request can take
// seconds when the gateway doesn't answer, so List never waits for one: it
// reads what is known and has the rest looked up in the background.
var macs = struct {
	sync.Mutex
	known    map[string]macEntry
	inflight map[string]bool
}{known: map[string]macEntry{}, inflight: map[string]bool{}}

const macTTL = 5 * time.Minute

// gatewayMAC is the MAC of gw as seen from ip, "" while unknown (being looked
// up, or an adapter without one: PPP, cellular, tunnels).
func gatewayMAC(ip, gw net.IP) string {
	key := ip.String() + "|" + gw.String()
	macs.Lock()
	defer macs.Unlock()
	e, ok := macs.known[key]
	if (!ok || time.Since(e.at) > macTTL) && !macs.inflight[key] {
		macs.inflight[key] = true
		go func() {
			mac := sendARP(ip, gw)
			macs.Lock()
			macs.known[key] = macEntry{mac, time.Now()}
			delete(macs.inflight, key)
			macs.Unlock()
		}()
	}
	return e.mac
}

func sendARP(src, dst net.IP) string {
	s, d := src.To4(), dst.To4()
	if s == nil || d == nil {
		return ""
	}
	var buf [8]byte
	n := uint32(len(buf))
	// IPAddr is the address's bytes in memory order.
	r, _, _ := procSendARP.Call(uintptr(binary.LittleEndian.Uint32(d)), uintptr(binary.LittleEndian.Uint32(s)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r != 0 || n == 0 || n > uint32(len(buf)) {
		return ""
	}
	return net.HardwareAddr(buf[:n]).String()
}

// SameRouter groups the interfaces that share a gateway MAC: id -> the ids of
// the others behind the same router. Interfaces without a known MAC share
// nothing.
func SameRouter(ifaces []Interface) map[string][]string {
	byMAC := map[string][]string{}
	for _, i := range ifaces {
		if i.GatewayMAC != "" {
			byMAC[i.GatewayMAC] = append(byMAC[i.GatewayMAC], i.ID)
		}
	}
	out := map[string][]string{}
	for _, ids := range byMAC {
		if len(ids) < 2 {
			continue
		}
		for _, id := range ids {
			for _, other := range ids {
				if other != id {
					out[id] = append(out[id], other)
				}
			}
		}
	}
	return out
}

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

// parseStunResponse is the IPv4 address a binding response maps to.
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
		if (typ == attrXorMapped || typ == attrMapped) && l >= 8 && v[1] == 0x01 { // IPv4
			ip := net.IP(append([]byte(nil), v[4:8]...))
			if typ == attrXorMapped {
				var m [4]byte
				binary.BigEndian.PutUint32(m[:], stunMagic)
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
// as the STUN server at server ("host:port") sees it.
func (b *Binding) PublicIP(ctx context.Context, server string) (net.IP, error) {
	host, port, err := net.SplitHostPort(server)
	if err != nil {
		return nil, err
	}
	ips, err := b.LookupIP(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("no IPv4 address for " + host)
	}
	addr, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(ips[0].String(), port))
	if err != nil {
		return nil, err
	}
	pc, err := b.ListenPacket(0)
	if err != nil {
		return nil, err
	}
	defer pc.Close()
	return stunQuery(ctx, pc, addr)
}
