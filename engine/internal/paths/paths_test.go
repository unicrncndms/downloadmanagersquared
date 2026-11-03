package paths

import (
	"slices"
	"testing"
)

func TestModeDecidesThePaths(t *testing.T) {
	wifi := Iface{ID: "wifi", Present: true, Enabled: true, Default: true}
	eth := Iface{ID: "eth", Present: true, Enabled: true}
	tests := []struct {
		name string
		mode Mode
		snap Snapshot
		want []string
		wait bool
	}{
		{"off uses bare interfaces, main first", Off,
			Snapshot{Ifaces: []Iface{eth, wifi}}, []string{"wifi", "eth"}, false},
		{"off ignores tunnels", Off,
			Snapshot{Ifaces: []Iface{up(eth), up(wifi)}}, []string{"wifi", "eth"}, false},
		{"alongside adds the tunnels that are up", Alongside,
			Snapshot{Ifaces: []Iface{up(eth), wifi}}, []string{"wifi", "eth", "eth~vpn"}, false},
		{"vpn only uses only tunnels, the main interface's first", VPNOnly,
			Snapshot{Ifaces: []Iface{up(eth), up(wifi)}}, []string{"wifi~vpn", "eth~vpn"}, false},
		{"vpn only without the main interface's tunnel leads with another", VPNOnly,
			Snapshot{Ifaces: []Iface{up(eth), wifi}}, []string{"eth~vpn"}, false},
		{"vpn only with no tunnel up waits", VPNOnly,
			Snapshot{Ifaces: []Iface{eth, wifi}}, nil, true},
		{"off with no interface does not wait", Off,
			Snapshot{}, nil, false},
		{"a chosen main interface leads", Off,
			Snapshot{Main: "eth", Ifaces: []Iface{eth, wifi}}, []string{"eth", "wifi"}, false},
		{"a disabled chosen main falls back to the default route", Off,
			Snapshot{Main: "eth", Ifaces: []Iface{{ID: "eth", Present: true}, wifi}}, []string{"wifi"}, false},
		{"without a default route the lowest ID leads", Off,
			Snapshot{Ifaces: []Iface{{ID: "b", Present: true, Enabled: true}, {ID: "a", Present: true, Enabled: true}}}, []string{"a", "b"}, false},
		{"a missing interface's tunnel is no path", VPNOnly,
			Snapshot{Ifaces: []Iface{{ID: "eth", Enabled: true, TunnelUp: true}}}, nil, true},
		{"a disabled interface's tunnel is no path", Alongside,
			Snapshot{Ifaces: []Iface{{ID: "eth", Present: true, TunnelUp: true}, wifi}}, []string{"wifi"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := For(tt.mode, tt.snap)
			if !slices.Equal(r.Paths, tt.want) || r.Wait != tt.wait {
				t.Fatalf("got %v wait=%v, want %v wait=%v", r.Paths, r.Wait, tt.want, tt.wait)
			}
		})
	}
}

func TestOldStateWithoutAModeReadsAsOff(t *testing.T) {
	if m := ParseMode("", Off); m != Off {
		t.Fatalf("got %q", m)
	}
	if m := ParseMode("bogus", Alongside); m != Alongside {
		t.Fatalf("got %q", m)
	}
}

func up(i Iface) Iface {
	i.TunnelUp = true
	return i
}
