package engine

import (
	"net"
	"reflect"
	"testing"

	"downloadmanagersquared/engine/internal/netif"
)

func TestInterfacesBehindOneRouterAreNamedButStayEnabled(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	gw := net.IPv4(192, 168, 1, 1)
	lo := net.IPv4(127, 0, 0, 1)
	e.mu.Lock()
	e.refreshInterfacesLocked([]netif.Interface{
		{ID: "eth", Name: "Ethernet", IPv4: lo, Gateway: gw, GatewayMAC: "aa:aa:aa:aa:aa:aa"},
		{ID: "wifi", Name: "Wi-Fi", IPv4: lo, Gateway: gw, GatewayMAC: "aa:aa:aa:aa:aa:aa"},
		{ID: "usb", Name: "Phone", IPv4: lo, Gateway: gw, GatewayMAC: "bb:bb:bb:bb:bb:bb"}, // another router, same gateway IP
	})
	e.mu.Unlock()
	for id, want := range map[string][]string{"eth": {"Wi-Fi"}, "wifi": {"Ethernet"}, "usb": nil} {
		v := ifaceView(t, e, id)
		if !reflect.DeepEqual(v.SameRouter, want) {
			t.Errorf("%s shares a router with %v, want %v", id, v.SameRouter, want)
		}
		if !v.Enabled {
			t.Errorf("%s was turned off", id)
		}
	}
}
