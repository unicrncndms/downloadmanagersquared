// Package netif finds the network adapters that can reach the internet on
// their own and builds dialers and HTTP clients pinned to one of them.
package netif

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Interface is one adapter with its own route to the internet, over IPv4,
// IPv6 or both.
type Interface struct {
	ID          string   `json:"id"` // adapter GUID, stable across reboots
	Index       uint32   `json:"index"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	IPv4        net.IP   `json:"ip"`                   // nil on an IPv6-only interface
	Gateway     net.IP   `json:"gateway"`              // the IPv4 default gateway, nil without IPv4
	IPv6        net.IP   `json:"ip6,omitempty"`        // global unicast; nil without IPv6 or with the setting off
	Gateway6    net.IP   `json:"gateway6,omitempty"`   // the IPv6 default gateway, usually link-local
	GatewayMAC  string   `json:"gatewayMac,omitempty"` // "" until known, or without one (PPP, tunnels)
	DNS         []net.IP `json:"dns"`                  // in Windows' order, only of the families the interface has
	VPN         bool     `json:"vpn"`
	IsDefault   bool     `json:"isDefault"` // carries the Windows default route
}

// Families are the address families the interface is used with: "IPv4",
// "IPv6" or both, in that order.
func (i Interface) Families() []string {
	var f []string
	if i.IPv4 != nil {
		f = append(f, "IPv4")
	}
	if i.IPv6 != nil {
		f = append(f, "IPv6")
	}
	return f
}

// IP is the address to show: IPv4, else IPv6.
func (i Interface) IP() net.IP {
	if i.IPv4 != nil {
		return i.IPv4
	}
	return i.IPv6
}

const (
	ifTypeSoftwareLoopback = 24
	ifTypePropVirtual      = 53
	ifTypeTunnel           = 131
)

var vpnPattern = regexp.MustCompile(`(?i)vpn|wintun|wireguard|\btap\b|tap-|tunnel|zerotier|tailscale|anyconnect`)

// List returns every adapter that is up and has an IPv4 address plus a default
// gateway; with ipv6 also every one with a global IPv6 address plus an IPv6
// default gateway, and their IPv6 addresses and DNS servers. Without ipv6
// nothing about IPv6 is kept, and ipv6Only names the adapters left out that
// have only IPv6, for the hint.
func List(ipv6 bool) (list []Interface, ipv6Only []string, err error) {
	head, err := adapterAddresses(windows.AF_UNSPEC)
	if err != nil {
		return nil, nil, err
	}
	list, ipv6Only = fromAdapters(head, ipv6)
	markDefault(list, bestInterface)
	return list, ipv6Only, nil
}

// fromAdapters is List's choice among the adapters Windows reports.
func fromAdapters(head *windows.IpAdapterAddresses, ipv6 bool) (list []Interface, ipv6Only []string) {
	for a := head; a != nil; a = a.Next {
		if a.OperStatus != windows.IfOperStatusUp || a.IfType == ifTypeSoftwareLoopback {
			continue
		}
		ip, gw := firstIPv4Unicast(a.FirstUnicastAddress), firstIPv4Gateway(a.FirstGatewayAddress)
		if ip == nil || gw == nil {
			ip, gw = nil, nil
		}
		ip6, gw6 := globalIPv6(a.FirstUnicastAddress), firstIPv6Gateway(a.FirstGatewayAddress)
		if ip6 == nil || gw6 == nil {
			ip6, gw6 = nil, nil
		}
		name := windows.UTF16PtrToString(a.FriendlyName)
		if !ipv6 && ip6 != nil {
			if ip == nil {
				ipv6Only = append(ipv6Only, name)
			}
			ip6, gw6 = nil, nil // with IPv6 off, as if the adapter had none
		}
		if ip == nil && ip6 == nil {
			continue
		}
		desc := windows.UTF16PtrToString(a.Description)
		iface := Interface{
			ID:          adapterID(a),
			Index:       a.IfIndex,
			Name:        name,
			Description: desc,
			IPv4:        ip,
			Gateway:     gw,
			IPv6:        ip6,
			Gateway6:    gw6,
			DNS:         dnsServers(a.FirstDnsServerAddress, ip != nil, ip6 != nil),
			VPN:         a.IfType == ifTypePropVirtual || a.IfType == ifTypeTunnel || vpnPattern.MatchString(desc) || vpnPattern.MatchString(name),
		}
		// Dual-stack interfaces keep ARP on IPv4.
		if ip != nil {
			iface.GatewayMAC = gatewayMAC(ip, gw, a.IfIndex)
		} else {
			iface.GatewayMAC = gatewayMAC(ip6, gw6, a.IfIndex)
		}
		list = append(list, iface)
	}
	return list, ipv6Only
}

// markDefault marks the interface with the IPv4 default route, or the one with
// the IPv6 default route when no interface has IPv4.
func markDefault(list []Interface, best func(windows.Sockaddr) (uint32, error)) {
	var target windows.Sockaddr = &windows.SockaddrInet4{Addr: [4]byte{1, 1, 1, 1}}
	if !slices.ContainsFunc(list, func(i Interface) bool { return i.IPv4 != nil }) {
		target = &windows.SockaddrInet6{Addr: [16]byte(net.ParseIP("2606:4700:4700::1111"))}
	}
	idx, err := best(target)
	if err != nil {
		return
	}
	for x := range list {
		list[x].IsDefault = list[x].Index == idx
	}
}

func bestInterface(sa windows.Sockaddr) (uint32, error) {
	var idx uint32
	err := windows.GetBestInterfaceEx(sa, &idx)
	return idx, err
}

func adapterID(a *windows.IpAdapterAddresses) string {

	return strings.ToLower(strings.Trim(windows.BytePtrToString(a.AdapterName), "{}"))
}

func adapterAddresses(family uint32) (*windows.IpAdapterAddresses, error) {

	size := uint32(16 * 1024)
	flags := uint32(windows.GAA_FLAG_INCLUDE_GATEWAYS | windows.GAA_FLAG_SKIP_ANYCAST | windows.GAA_FLAG_SKIP_MULTICAST)
	for range 5 {
		buf := make([]byte, size)
		aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(family, flags, 0, aa, &size)
		if err == nil {
			return aa, nil
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			return nil, fmt.Errorf("GetAdaptersAddresses: %w", err)
		}
	}
	return nil, fmt.Errorf("GetAdaptersAddresses: buffer keeps growing")
}

func firstIPv4Unicast(u *windows.IpAdapterUnicastAddress) net.IP {
	for ; u != nil; u = u.Next {
		if ip := u.Address.IP().To4(); ip != nil && !ip.IsLinkLocalUnicast() {
			return ip
		}
	}
	return nil
}

func firstIPv4Gateway(g *windows.IpAdapterGatewayAddress) net.IP {
	for ; g != nil; g = g.Next {
		if ip := g.Address.IP().To4(); ip != nil && !ip.IsUnspecified() {
			return ip
		}
	}
	return nil
}

// globalIPv6 is the adapter's first usable global IPv6 address: not
// link-local, not unique local (fc00::/7), not a temporary (privacy) address
// and not tentative or deprecated.
func globalIPv6(u *windows.IpAdapterUnicastAddress) net.IP {
	for ; u != nil; u = u.Next {
		ip := u.Address.IP()
		if !isIPv6(ip) || !ip.IsGlobalUnicast() || ip.IsPrivate() || u.DadState != windows.IpDadStatePreferred {
			continue
		}
		if u.PrefixOrigin == windows.IpPrefixOriginRouterAdvertisement && u.SuffixOrigin == windows.IpSuffixOriginRandom {
			continue // temporary: it changes every day, and a server may tie a link to its address
		}
		return ip
	}
	return nil
}

func firstIPv6Gateway(g *windows.IpAdapterGatewayAddress) net.IP {
	for ; g != nil; g = g.Next {
		if ip := g.Address.IP(); isIPv6(ip) && !ip.IsUnspecified() {
			return ip
		}
	}
	return nil
}

// isIPv6 is an IPv6 address that isn't an IPv4 one written as IPv6.
func isIPv6(ip net.IP) bool { return len(ip) == net.IPv6len && ip.To4() == nil }

// dnsServers are the adapter's DNS servers of the families asked for, in
// Windows' order, each once. fec0::/10 (site-local) is left out: Windows puts
// fec0:0:0:ffff::1-3 on adapters that have no IPv6 DNS server.
func dnsServers(d *windows.IpAdapterDnsServerAdapter, v4, v6 bool) []net.IP {
	var out []net.IP
	for ; d != nil; d = d.Next {
		ip := d.Address.IP()
		switch {
		case ip.To4() != nil:
			if !v4 {
				continue
			}
			ip = ip.To4()
		case !v6 || !isIPv6(ip) || ip[0] == 0xfe && ip[1]&0xc0 == 0xc0:
			continue
		}
		if !slices.ContainsFunc(out, ip.Equal) {
			out = append(out, ip)
		}
	}
	return out
}

// PublicIPv4 is whether ip is reachable from the internet as it is: not private,
// shared (carrier NAT), loopback, link-local (so not 169.254.169.254 either),
// unspecified, multicast or reserved.
func PublicIPv4(ip net.IP) bool {
	ip = ip.To4()
	if ip == nil || ip[0] == 0 || ip[0] >= 224 {
		return false
	}
	cgnat := net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}
	return !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !cgnat.Contains(ip)
}
