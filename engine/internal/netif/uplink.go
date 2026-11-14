package netif

import (
	"encoding/binary"
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
	iphlpapi               = windows.NewLazySystemDLL("iphlpapi.dll")
	procSendARP            = iphlpapi.NewProc("SendARP")
	procResolveIpNetEntry2 = iphlpapi.NewProc("ResolveIpNetEntry2")
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

const (
	macTTL   = 5 * time.Minute
	macRetry = 30 * time.Second // a failed lookup (no answer) is tried again sooner
)

// lookupMAC asks the gateway gw, seen from ip on interface index, for its MAC.
var lookupMAC = func(ip, gw net.IP, index uint32) string {
	if gw.To4() != nil {
		return sendARP(ip, gw)
	}
	return resolveNeighbor(gw, index)
}

// gatewayMAC is the MAC of gw as seen from ip on interface index, "" while
// unknown (being looked up, or an adapter without one: PPP, cellular,
// tunnels). An IPv4 gateway is asked with ARP, an IPv6 one with neighbor
// discovery.
func gatewayMAC(ip, gw net.IP, index uint32) string {
	key := ip.String() + "|" + gw.String()
	macs.Lock()
	defer macs.Unlock()
	e, ok := macs.known[key]
	ttl := macTTL
	if e.mac == "" {
		ttl = macRetry
	}
	if (!ok || time.Since(e.at) > ttl) && !macs.inflight[key] {
		macs.inflight[key] = true
		go func() {
			mac := lookupMAC(ip, gw, index)
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

// mibIpnetRow2 is MIB_IPNET_ROW2 from netioapi.h (88 bytes).
type mibIpnetRow2 struct {
	Address               [28]byte // SOCKADDR_INET, here a sockaddr_in6
	InterfaceIndex        uint32
	InterfaceLuid         uint64
	PhysicalAddress       [32]byte
	PhysicalAddressLength uint32
	State                 uint32
	Flags                 uint8
	_                     [3]byte
	ReachabilityTime      uint32
}

// resolveNeighbor is the MAC of the IPv6 neighbor ip on interface index,
// found with neighbor discovery; "" if it doesn't answer.
func resolveNeighbor(ip net.IP, index uint32) string {
	var row mibIpnetRow2
	binary.LittleEndian.PutUint16(row.Address[0:], windows.AF_INET6)
	copy(row.Address[8:24], ip.To16())
	binary.LittleEndian.PutUint32(row.Address[24:], index) // scope: a gateway is link-local
	row.InterfaceIndex = index
	r, _, _ := procResolveIpNetEntry2.Call(uintptr(unsafe.Pointer(&row)), 0)
	if r != 0 || row.PhysicalAddressLength == 0 || row.PhysicalAddressLength > uint32(len(row.PhysicalAddress)) {
		return ""
	}
	return net.HardwareAddr(row.PhysicalAddress[:row.PhysicalAddressLength]).String()
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

// LineOf is what Check lines compares for a public address: an IPv4 address
// as it is, an IPv6 one by its /64 (one customer's network), so two IPv6-only
// lines from one uplink match although each has its own address.
func LineOf(ip net.IP) string {
	if ip.To4() != nil {
		return ip.String()
	}
	return (&net.IPNet{IP: ip.Mask(net.CIDRMask(64, 128)), Mask: net.CIDRMask(64, 128)}).String()
}
