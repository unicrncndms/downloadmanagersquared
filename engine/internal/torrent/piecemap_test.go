package torrent

import (
	"slices"
	"testing"
)

func TestPieceMapDrawsWhoBroughtEachPiece(t *testing.T) {
	// Four chosen pieces of five: 0 from before this run, 1 by eth, 2 being received by wifi, 3 missing; 4 skipped.
	complete := []bool{true, true, false, false, true}
	partial := []bool{false, false, true, false, false}
	want := []bool{true, true, true, true, false}
	first := []string{"", "eth", "wifi", "", "eth"}
	cells, by, part := pieceMap(complete, partial, want, first)
	if cells != "#a1." || !slices.Equal(by, []string{"eth", "wifi"}) || part != "0-0-0-0-" {
		t.Fatalf("got %q %v %q", cells, by, part)
	}
}

func TestPieceMapSquaresHoldSeveralPieces(t *testing.T) {
	n := 2 * mapCells
	complete, partial, want, first := make([]bool, n), make([]bool, n), make([]bool, n), make([]string, n)
	for i := range n {
		want[i] = true
	}
	complete[0], first[0] = true, "eth" // the first square half in
	cells, _, part := pieceMap(complete, partial, want, first)
	if len(cells) != mapCells || cells[0] != '.' || part[:2] != "5a" {
		t.Fatalf("got %q… part %q…", cells[:4], part[:4])
	}
	if c, _, _ := pieceMap(nil, nil, nil, nil); c != "" {
		t.Fatalf("nothing chosen drew %q", c)
	}
}
