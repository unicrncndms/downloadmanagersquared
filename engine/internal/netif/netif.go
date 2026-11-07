// Package netif finds the network adapters that can reach the internet on
// their own and builds dialers and HTTP clients pinned to one of them.
package netif

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Interface is one adapter with its own route to the internet.
type Interface struct {
	ID          string   `json:"id"` // adapter GUID, stable across reboots
	Index       uint32   `json:"index"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	IPv4        net.IP   `json:"ip"`
	Gateway     net.IP   `json:"gateway"`
	GatewayMAC  string   `json:"gatewayMac,omitempty"` // "" until known, or without one (PPP, tunnels)
	DNS         []net.IP `json:"dns"`
	VPN         bool     `json:"vpn"`
	IsDefault   bool     `json:"isDefault"` // carries the Windows default route
}

const (
	ifTypeSoftwareLoopback = 24
	ifTypePropVirtual      = 53
	ifTypeTunnel           = 131
)

var vpnPattern = regexp.MustCompile(`(?i)vpn|wintun|wireguard|\btap\b|tap-|tunnel|zerotier|tailscale|anyconnect`)

// List returns every adapter that is up and has an IPv4 address plus a default gateway.
func List() ([]Interface, error) {
	head, err := adapterAddresses()
	if err != nil {
		return nil, err
	}
	defIdx := defaultIndex()

	var out []Interface
	for a := head; a != nil; a = a.Next {
		if a.OperStatus != windows.IfOperStatusUp || a.IfType == ifTypeSoftwareLoopback {
			continue
		}
		ip := firstIPv4Unicast(a.FirstUnicastAddress)
		gw := firstIPv4Gateway(a.FirstGatewayAddress)
		if ip == nil || gw == nil {
			continue
		}
		id := strings.ToLower(strings.Trim(windows.BytePtrToString(a.AdapterName), "{}"))
		desc := windows.UTF16PtrToString(a.Description)
		name := windows.UTF16PtrToString(a.FriendlyName)
		iface := Interface{
			ID:          id,
			Index:       a.IfIndex,
			Name:        name,
			Description: desc,
			IPv4:        ip,
			Gateway:     gw,
			GatewayMAC:  gatewayMAC(ip, gw),
			DNS:         dnsServers(a.FirstDnsServerAddress),
			VPN:         a.IfType == ifTypePropVirtual || a.IfType == ifTypeTunnel || vpnPattern.MatchString(desc) || vpnPattern.MatchString(name),
			IsDefault:   a.IfIndex == defIdx,
		}
		out = append(out, iface)
	}
	return out, nil
}

func adapterAddresses() (*windows.IpAdapterAddresses, error) {
	size := uint32(16 * 1024)
	flags := uint32(windows.GAA_FLAG_INCLUDE_GATEWAYS | windows.GAA_FLAG_SKIP_ANYCAST | windows.GAA_FLAG_SKIP_MULTICAST)
	for range 5 {
		buf := make([]byte, size)
		aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_INET, flags, 0, aa, &size)
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

func dnsServers(d *windows.IpAdapterDnsServerAdapter) []net.IP {
	var out []net.IP
	for ; d != nil; d = d.Next {
		if ip := d.Address.IP().To4(); ip != nil {
			out = append(out, ip)
		}
	}
	return out
}

func defaultIndex() uint32 {
	var idx uint32
	if err := windows.GetBestInterfaceEx(&windows.SockaddrInet4{Addr: [4]byte{1, 1, 1, 1}}, &idx); err != nil {
		return 0
	}
	return idx
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
