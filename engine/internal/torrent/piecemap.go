package torrent

import "slices"

// mapCells is how many squares the pages draw a download in, as for an HTTP
// download (download's fileMap).
const mapCells = 96

// pieceMap is a torrent's chosen pieces as squares, in the format of an HTTP
// download's map (download.Summary Map, MapBy, MapPart), for the pages' grid:
// '#' all on disk from before this run, a letter all on disk and first
// received by line by[letter], a digit where a piece of the square is being
// received now by line by[digit], '.' some of it missing. part says, for a
// square some of whose pieces are in, how much (a digit in tenths) and which
// line brought most of them. complete, partial and first are per piece (first:
// the line ID that first received blocks of it, "" for none); want marks the
// chosen pieces. All "" when none is chosen.
func pieceMap(complete, partial, want []bool, first []string) (cells string, by []string, part string) {
	var w []int
	for i, ok := range want {
		if ok && i < len(complete) {
			w = append(w, i)
		}
	}
	if len(w) == 0 {
		return "", nil, ""
	}
	n := min(mapCells, len(w))
	// by's place for a line, or -1 once ten are taken (a digit each).
	place := func(id string) int {
		if id == "" {
			return -1
		}
		k := slices.Index(by, id)
		if k < 0 && len(by) < 10 {
			by, k = append(by, id), len(by)
		}
		return k
	}
	who := func(i int) string {
		if i < len(first) {
			return first[i]
		}
		return ""
	}
	c := make([]byte, n)
	p := make([]byte, 0, 2*n)
	for k := range n {
		ps := w[k*len(w)/n : (k+1)*len(w)/n]
		done, now := 0, -1
		count := map[string]int{}
		for _, i := range ps {
			switch {
			case complete[i]:
				done++
				count[who(i)]++
			case partial[i] && now < 0:
				now = place(who(i))
			}
		}
		// The line that brought most of the square's pieces in this run.
		most, mostN := "", 0
		for id, m := range count {
			if id != "" && (m > mostN || m == mostN && id < most) {
				most, mostN = id, m
			}
		}
		d, wb := byte('0'), byte('-')
		switch {
		case done == len(ps):
			c[k] = '#'
			if l := place(most); l >= 0 && mostN*2 >= len(ps) {
				c[k] = byte('a' + l)
			}
		case now >= 0:
			c[k] = byte('0' + now)
		default:
			c[k] = '.'
		}
		if done > 0 && done < len(ps) {
			d = byte('0' + min(9, max(1, done*10/len(ps))))
			if l := place(most); l >= 0 {
				wb = byte('a' + l)
			}
		}
		p = append(p, d, wb)
	}
	return string(c), by, string(p)
}
