package download

import (
	"cmp"
	"slices"

	"downloadmanagersquared/engine/internal/archive"
)

// onDisk: none of [start, end) is missing.
func onDisk(missing []Range, start, end int64) bool {
	for _, r := range missing {
		if r.Start < end && start < r.End {
			return false
		}
	}
	return true
}

// mergeRanges sorts ranges and joins those that touch.
func mergeRanges(rs []Range) []Range {
	slices.SortFunc(rs, func(a, b Range) int { return cmp.Compare(a.Start, b.Start) })
	out := rs[:0]
	for _, r := range rs {
		if n := len(out); n > 0 && r.Start <= out[n-1].End {
			out[n-1].End = max(out[n-1].End, r.End)
			continue
		}
		out = append(out, r)
	}
	return out
}

// subtractRanges is rs without the bytes in cut. rs is left alone.
func subtractRanges(rs, cut []Range) []Range {
	out := slices.Clone(rs)
	for _, c := range cut {
		var next []Range
		for _, r := range out {
			if c.End <= r.Start || r.End <= c.Start {
				next = append(next, r)
				continue
			}
			if r.Start < c.Start {
				next = append(next, Range{r.Start, c.Start})
			}
			if c.End < r.End {
				next = append(next, Range{c.End, r.End})
			}
		}
		out = next
	}
	return out
}

// onDiskA is onDisk for the archive's ranges: the two Range types differ in
// their JSON tags, so one function can't take both.
func onDiskA(missing []archive.Range, start, end int64) bool {
	for _, r := range missing {
		if r.Start < end && start < r.End {
			return false
		}
	}
	return true
}

func intersectRanges(a, b []Range) []Range {
	var out []Range
	for _, x := range a {
		for _, y := range b {
			if lo, hi := max(x.Start, y.Start), min(x.End, y.End); lo < hi {
				out = append(out, Range{lo, hi})
			}
		}
	}
	return out
}

func sumRanges(rs []Range) int64 {
	var n int64
	for _, r := range rs {
		n += r.End - r.Start
	}
	return n
}
