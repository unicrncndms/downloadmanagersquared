package engine

import (
	"testing"

	"downloadmanagersquared/engine/internal/netif"
)

func TestSettingsHintsWhenWindowsWouldDropWiFiForAnotherLine(t *testing.T) {
	wifi := []string{"wlan"}
	cable := netif.Interface{ID: "eth"}
	air := netif.Interface{ID: "wlan"}
	vpn := netif.Interface{ID: "pia", VPN: true}
	for _, c := range []struct {
		name      string
		minimizes bool
		wifi      []string
		list      []netif.Interface
		want      bool
	}{
		{"a cable up and Wi-Fi dropped", true, wifi, []netif.Interface{cable}, true},
		{"a cable and Wi-Fi both up", true, wifi, []netif.Interface{cable, air}, true},
		{"Windows keeps every connection", false, wifi, []netif.Interface{cable, air}, false},
		{"Wi-Fi alone", true, wifi, []netif.Interface{air}, false},
		{"Wi-Fi and a VPN", true, wifi, []netif.Interface{air, vpn}, false},
		{"no Wi-Fi adapter", true, nil, []netif.Interface{cable}, false},
	} {
		if got := dropsWiFi(c.minimizes, c.wifi, c.list); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
