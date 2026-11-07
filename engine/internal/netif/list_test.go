package netif

import (
	"net"
	"reflect"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// adapter is one entry of GetAdaptersAddresses' answer, for fromAdapters.
type adapter struct {
	id, name string
	unicast  []unicast
	gateways []string
	dns      []string
}

type unicast struct {
	ip                  string
	prefix, suffix, dad int32
}

func sockaddr(s string) windows.SocketAddress {
	ip := net.ParseIP(s)
	any := new(syscall.RawSockaddrAny)
	if ip4 := ip.To4(); ip4 != nil {
		sa := (*windows.RawSockaddrInet4)(unsafe.Pointer(any))
		sa.Family = windows.AF_INET
		copy(sa.Addr[:], ip4)
		return windows.SocketAddress{Sockaddr: any, SockaddrLength: int32(unsafe.Sizeof(*sa))}
	}
	sa := (*windows.RawSockaddrInet6)(unsafe.Pointer(any))
	sa.Family = windows.AF_INET6
	copy(sa.Addr[:], ip)
	return windows.SocketAddress{Sockaddr: any, SockaddrLength: int32(unsafe.Sizeof(*sa))}
}

func adapters(list ...adapter) *windows.IpAdapterAddresses {
	var head *windows.IpAdapterAddresses
	for x := len(list) - 1; x >= 0; x-- {
		a := list[x]
		name, _ := windows.UTF16PtrFromString(a.name)
		id, _ := windows.BytePtrFromString("{" + a.id + "}")
		w := &windows.IpAdapterAddresses{Next: head, IfIndex: uint32(x + 1), AdapterName: id, FriendlyName: name, OperStatus: windows.IfOperStatusUp}
		for y := len(a.unicast) - 1; y >= 0; y-- {
			u := a.unicast[y]
			if u.dad == 0 {
				u.dad = windows.IpDadStatePreferred
			}
			w.FirstUnicastAddress = &windows.IpAdapterUnicastAddress{Next: w.FirstUnicastAddress, Address: sockaddr(u.ip), PrefixOrigin: u.prefix, SuffixOrigin: u.suffix, DadState: u.dad}
		}
		for y := len(a.gateways) - 1; y >= 0; y-- {
			w.FirstGatewayAddress = &windows.IpAdapterGatewayAddress{Next: w.FirstGatewayAddress, Address: sockaddr(a.gateways[y])}
		}
		for y := len(a.dns) - 1; y >= 0; y-- {
			w.FirstDnsServerAddress = &windows.IpAdapterDnsServerAdapter{Next: w.FirstDnsServerAddress, Address: sockaddr(a.dns[y])}
		}
		head = w
	}
	return head
}

const ra, random, link = windows.IpPrefixOriginRouterAdvertisement, windows.IpSuffixOriginRandom, windows.IpSuffixOriginLinkLayerAddress

var testAdapters = []adapter{
	{id: "v4", name: "Ethernet", unicast: []unicast{{ip: "192.168.1.20"}}, gateways: []string{"192.168.1.1"}, dns: []string{"192.168.1.1"}},
	// Dual-stack, as Windows reports WLAN 2: link-local, temporary and public
	// addresses, a ULA and a global DNS server, each twice, behind the IPv4 one.
	{id: "dual", name: "WLAN 2", unicast: []unicast{
		{ip: "fe80::650b:59b1:94ea:c416"},
		{ip: "2a02:2c60:f110:a80:dc77:7e81:d623:5d50", prefix: ra, suffix: random},
		{ip: "2a02:2c60:f110:a80:1e29:3239:c102:ce4a", prefix: ra, suffix: link},
		{ip: "192.168.2.33"},
	}, gateways: []string{"fe80::5e49:79ff:fedb:777f", "192.168.2.1"},
		dns: []string{"fd00::1", "2a02:2c60:f110:a80::1", "fd00::1", "192.168.2.1", "2a02:2c60:f110:a80::1"}},
	// IPv6 only: a deprecated address first, then the one to use; Windows'
	// fec0 placeholders are no DNS servers.
	{id: "v6", name: "Phone", unicast: []unicast{
		{ip: "2001:470:1::5", dad: windows.IpDadStateDeprecated},
		{ip: "2001:470:1::6", prefix: windows.IpPrefixOriginDhcp, suffix: windows.IpSuffixOriginDhcp},
	}, gateways: []string{"fe80::1"}, dns: []string{"fec0:0:0:ffff::1", "fec0:0:0:ffff::2"}},
	// Only link-local and unique local IPv6: no internet.
	{id: "ula", name: "Lab", unicast: []unicast{{ip: "fe80::2"}, {ip: "fd12::3"}}, gateways: []string{"fe80::1"}},
	// Only a temporary global address.
	{id: "temp", name: "Temp", unicast: []unicast{{ip: "2001:470:2::7", prefix: ra, suffix: random}}, gateways: []string{"fe80::1"}},
}

func ips(s ...string) []net.IP {
	var out []net.IP
	for _, x := range s {
		ip := net.ParseIP(x)
		if ip4 := ip.To4(); ip4 != nil {
			ip = ip4
		}
		out = append(out, ip)
	}
	return out
}

func TestWithIPv6OffTheListIsIPv4OnlyAndIPv6OnlyAdaptersAreNamed(t *testing.T) {
	list, only := fromAdapters(adapters(testAdapters...), false)
	want := []Interface{
		{ID: "v4", Index: 1, Name: "Ethernet", IPv4: ips("192.168.1.20")[0], Gateway: ips("192.168.1.1")[0], DNS: ips("192.168.1.1")},
		{ID: "dual", Index: 2, Name: "WLAN 2", IPv4: ips("192.168.2.33")[0], Gateway: ips("192.168.2.1")[0], DNS: ips("192.168.2.1")},
	}
	for x := range list {
		list[x].GatewayMAC = "" // looked up in the background
	}
	if !reflect.DeepEqual(list, want) {
		t.Errorf("list\n got %+v\nwant %+v", list, want)
	}
	if !reflect.DeepEqual(only, []string{"Phone"}) {
		t.Errorf("IPv6-only adapters %v, want [Phone]", only)
	}
}

func TestWithIPv6OnAdaptersGetTheirGlobalAddressGatewayAndDNS(t *testing.T) {
	list, only := fromAdapters(adapters(testAdapters...), true)
	if only != nil {
		t.Errorf("IPv6-only adapters %v with IPv6 on", only)
	}
	got := map[string]Interface{}
	for _, i := range list {
		got[i.ID] = i
	}
	if len(got) != 3 {
		t.Fatalf("listed %v, want v4, dual and v6", list)
	}
	if v4 := got["v4"]; v4.IPv6 != nil || !reflect.DeepEqual(v4.Families(), []string{"IPv4"}) {
		t.Errorf("v4: %+v", v4)
	}
	d := got["dual"]
	if !d.IPv6.Equal(net.ParseIP("2a02:2c60:f110:a80:1e29:3239:c102:ce4a")) || !d.Gateway6.Equal(net.ParseIP("fe80::5e49:79ff:fedb:777f")) {
		t.Errorf("dual-stack: address %v gateway %v, want the public (not temporary) one and the router", d.IPv6, d.Gateway6)
	}
	if !reflect.DeepEqual(d.DNS, ips("fd00::1", "2a02:2c60:f110:a80::1", "192.168.2.1")) {
		t.Errorf("dual-stack DNS %v, want Windows' order, each once", d.DNS)
	}
	if !reflect.DeepEqual(d.Families(), []string{"IPv4", "IPv6"}) || !d.IP().Equal(d.IPv4) {
		t.Errorf("dual-stack families %v, shown address %v", d.Families(), d.IP())
	}
	v6 := got["v6"]
	if v6.IPv4 != nil || v6.Gateway != nil || !v6.IPv6.Equal(net.ParseIP("2001:470:1::6")) || len(v6.DNS) != 0 {
		t.Errorf("IPv6-only: %+v", v6)
	}
	if !reflect.DeepEqual(v6.Families(), []string{"IPv6"}) || !v6.IP().Equal(v6.IPv6) {
		t.Errorf("IPv6-only families %v, shown address %v", v6.Families(), v6.IP())
	}
}

func TestTheIPv6DefaultRouteIsMainOnlyWhenNoInterfaceHasIPv4(t *testing.T) {
	// The IPv4 route leaves through 1, the IPv6 one through 2.
	best := func(sa windows.Sockaddr) (uint32, error) {
		if _, v6 := sa.(*windows.SockaddrInet6); v6 {
			return 2, nil
		}
		return 1, nil
	}
	v4 := Interface{Index: 1, IPv4: ips("192.168.1.20")[0]}
	v6 := Interface{Index: 2, IPv6: net.ParseIP("2001:470:1::6")}
	list := []Interface{v4, v6}
	markDefault(list, best)
	if !list[0].IsDefault || list[1].IsDefault {
		t.Errorf("with an IPv4 interface the IPv4 route decides: %+v", list)
	}
	list = []Interface{v6}
	markDefault(list, best)
	if !list[0].IsDefault {
		t.Errorf("with IPv6 only the IPv6 route decides: %+v", list)
	}
}
