package rate

import "testing"

func TestBinding(t *testing.T) {
	c, ok := Binding(Cap{"this download's limit", 0, 9e6}, Cap{"Wi-Fi's limit", 5e6, 4.9e6}, Cap{"the limit for all connections", 6e6, 5.9e6})
	if !ok || c.Name != "Wi-Fi's limit" {
		t.Fatalf("got %v %v; want Wi-Fi's limit", c, ok)
	}
	if _, ok := Binding(Cap{"x", 5e6, 1e6}); ok {
		t.Fatal("a limit far above the speed was blamed")
	}
}
