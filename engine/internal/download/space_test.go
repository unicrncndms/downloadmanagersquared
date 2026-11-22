package download

import (
	"strings"
	"testing"
)

// A small drive with room for the file but not for the reserve says why it pauses.
func TestCheckSpaceNamesTheReserve(t *testing.T) {
	old := freeSpace
	defer func() { freeSpace = old }()
	freeSpace = func(string) (int64, error) { return 53 << 20, nil }
	err := checkSpace(`Y:\bench`, 34<<20)
	if err == nil || !strings.Contains(err.Error(), "kept free") {
		t.Fatalf("got %v, want a pause that names the space kept free", err)
	}
	freeSpace = func(string) (int64, error) { return 1 << 30, nil }
	if err := checkSpace(`Y:\bench`, 34<<20); err != nil {
		t.Fatalf("enough room: %v", err)
	}
}
