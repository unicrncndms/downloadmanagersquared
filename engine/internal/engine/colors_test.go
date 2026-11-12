package engine

import (
	"net"
	"testing"

	"downloadmanagersquared/engine/internal/netif"
)

func ifaceAt(id, name string) netif.Interface {
	return netif.Interface{ID: id, Name: name, IPv4: net.IPv4(127, 0, 0, 1)}
}

func colorOf(t *testing.T, e *Engine, id string) int {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	is := e.settings.Interfaces[id]
	if is == nil || is.Color == nil {
		t.Fatalf("%s has no colour", id)
	}
	return *is.Color
}

func TestColorsAreGivenInTheOrderThePagesListThemAndNeverChange(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	// A user from before colours: both interfaces are remembered without one.
	// The pages listed them by name, so Ethernet was 0 and Wi-Fi 1.
	e.mu.Lock()
	e.refreshInterfacesLocked([]netif.Interface{ifaceAt("wifi", "Wi-Fi"), ifaceAt("eth", "Ethernet")})
	e.mu.Unlock()
	if c := [2]int{colorOf(t, e, "eth"), colorOf(t, e, "wifi")}; c != [2]int{0, 1} {
		t.Fatalf("colours = %v, want Ethernet 0 and Wi-Fi 1", c)
	}
	// One that sorts first appears: the others keep theirs, it takes the lowest free.
	e.mu.Lock()
	e.refreshInterfacesLocked([]netif.Interface{ifaceAt("wifi", "Wi-Fi"), ifaceAt("eth", "Ethernet"), ifaceAt("a", "Alpha")})
	e.mu.Unlock()
	if c := [3]int{colorOf(t, e, "eth"), colorOf(t, e, "wifi"), colorOf(t, e, "a")}; c != [3]int{0, 1, 2} {
		t.Fatalf("colours = %v, want 0, 1 and 2 for the new one", c)
	}
	// A remembered but absent interface keeps its colour taken.
	e.mu.Lock()
	e.refreshInterfacesLocked([]netif.Interface{ifaceAt("eth", "Ethernet"), ifaceAt("b", "Beta")})
	e.mu.Unlock()
	if c := colorOf(t, e, "b"); c != 3 {
		t.Fatalf("new colour = %d, want 3", c)
	}
	if v := ifaceView(t, e, "eth"); v.Color != 0 {
		t.Fatalf("view colour = %d, want 0", v.Color)
	}
}

func TestSeventhInterfaceSharesAColour(t *testing.T) {
	s := Settings{Interfaces: map[string]*IfaceSettings{}}
	var ids []string
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		s.Interfaces[id] = &IfaceSettings{}
		ids = append(ids, id)
	}
	s.assignColors(ids)
	for i, id := range ids {
		if want := i % colorCount; *s.Interfaces[id].Color != want {
			t.Fatalf("%s = %d, want %d", id, *s.Interfaces[id].Color, want)
		}
	}
}

func TestSetInterfaceColorIsValidated(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	e.mu.Lock()
	e.refreshInterfacesLocked([]netif.Interface{ifaceAt("eth", "Ethernet")})
	e.mu.Unlock()
	set := func(body string) {
		t.Helper()
		if _, err := e.Call(nil, "setInterface", []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	set(`{"id":"eth","color":4}`)
	if c := colorOf(t, e, "eth"); c != 4 {
		t.Fatalf("colour = %d, want 4", c)
	}
	for _, bad := range []string{`{"id":"eth","color":6}`, `{"id":"eth","color":-1}`} {
		set(bad)
		if c := colorOf(t, e, "eth"); c != 4 {
			t.Fatalf("%s changed the colour to %d", bad, c)
		}
	}
	// A shared colour is allowed.
	e.mu.Lock()
	e.refreshInterfacesLocked([]netif.Interface{ifaceAt("eth", "Ethernet"), ifaceAt("wifi", "Wi-Fi")})
	e.mu.Unlock()
	set(`{"id":"wifi","color":4}`)
	if colorOf(t, e, "wifi") != 4 {
		t.Fatal("two connections can't share a colour")
	}
}
