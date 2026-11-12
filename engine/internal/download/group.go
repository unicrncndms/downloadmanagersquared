package download

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// GroupSummary is what the UIs show for the downloads that share a Group: the
// files of one Metalink, or the parts of a split archive. Like Present for one
// download, it holds what the group means to a user; the UIs only lay it out.
type GroupSummary struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Parts bool    `json:"parts"` // a split archive's parts, not a Metalink's files
	Count int     `json:"count"` // how many it has: its Set knows before every part is added
	Items int     `json:"items"` // how many are listed now
	Done  int     `json:"done"`  // of those, completed
	Size  int64   `json:"size"`  // -1 while not known
	Got   int64   `json:"got"`
	Speed float64 `json:"speed"`
	ETA   float64 `json:"eta"` // seconds until all of it is fetched at this speed, -1 when unknown

	// A split archive's extraction can't end before its last byte arrives:
	// the later of its own pace and ETA, in seconds, 0 while unknown.
	ExtractETA float64 `json:"extractEta,omitempty"`

	Finished bool   `json:"finished"` // every file fetched and, for a split archive, extracted
	Label    string `json:"label"`
	Tone     string `json:"tone,omitempty"`

	Pause       bool `json:"pause"`
	Resume      bool `json:"resume"`
	PartialFile bool `json:"partialFile"`

	// The Set's extraction count, while every part being fetched isn't the end.
	Files int `json:"files,omitempty"`
	Of    int `json:"of,omitempty"`

	Order []string `json:"order"` // the ids, as they are listed under the group's row
}

// Groups summarizes the downloads that share a Group, for each Group with at
// least two of them (one alone is drawn as a plain row). In first-seen order.
func Groups(sums []Summary) []GroupSummary {
	var ids []string
	by := map[string][]Summary{}
	for _, s := range sums {
		if s.Group == "" {
			continue
		}
		if _, ok := by[s.Group]; !ok {
			ids = append(ids, s.Group)
		}
		by[s.Group] = append(by[s.Group], s)
	}
	out := []GroupSummary{}
	for _, id := range ids {
		if len(by[id]) > 1 {
			out = append(out, summarizeGroup(id, by[id]))
		}
	}
	return out
}

func summarizeGroup(id string, items []Summary) GroupSummary {
	// What needs you first, then downloading, seeding, queued, paused, and last
	// whatever is completed (a part waiting for its archive too); by name within
	// each, so part 2 comes before part 10.
	rank := func(s Summary) int {
		if s.Status == StatusCompleted {
			return len(sectionRank)
		}
		return sectionRank[s.Section]
	}
	items = append([]Summary(nil), items...)
	sort.SliceStable(items, func(i, j int) bool {
		if ri, rj := rank(items[i]), rank(items[j]); ri != rj {
			return ri < rj
		}
		return numericLess(items[i].Filename, items[j].Filename)
	})

	g := GroupSummary{ID: id, Parts: strings.HasPrefix(id, "parts:"), Items: len(items), Order: []string{}}
	var set *SetSummary
	known := true
	for _, s := range items {
		g.Order = append(g.Order, s.ID)
		if g.Name == "" {
			g.Name = s.GroupName
		}
		if set == nil && s.Folder != nil {
			set = s.Folder.Set
		}
		if s.Size > 0 {
			g.Size += s.Size
		} else {
			known = false
		}
		g.Got += s.Done
		if s.Status == StatusRunning {
			g.Speed += s.Speed
		}
		if s.Status == StatusCompleted {
			g.Done++
		}
		g.Pause = g.Pause || s.Actions.Pause
		g.Resume = g.Resume || s.Actions.Resume
		g.PartialFile = g.PartialFile || s.PartialFile
	}
	if g.Name == "" {
		g.Name = "Metalink"
	}
	// A Set knows how many parts its archive has once they are laid out; RAR
	// volumes are laid out as each is probed, so the parts listed may be more.
	g.Count = g.Items
	if set != nil {
		g.Count = max(g.Count, set.Parts)
	}
	// A split archive's Set knows about how long the parts are all together
	// before each has started.
	if !known {
		g.Size = -1
		if set != nil && set.Total > 0 {
			g.Size = set.Total
		}
	}

	g.ETA = -1
	if g.Speed > 0 && g.Size > g.Got {
		g.ETA = float64(g.Size-g.Got) / g.Speed
	}

	if set != nil && set.ETA > 0 {
		g.ExtractETA = math.Round(max(set.ETA, g.ETA))
	}

	waiting := set != nil && !set.Finished // every part fetched isn't the end: extracting, or a part missing
	fetched := g.Done == g.Items
	g.Finished = fetched && !waiting
	g.Label, g.Tone = items[0].Label, items[0].Tone
	switch {
	case g.Finished:
		g.Label, g.Tone = "Completed", toneOK
	case fetched && set.Note != "":
		g.Label, g.Tone = "Waiting", toneWarn
	case fetched:
		g.Label, g.Tone = "Extracting", toneNone
	}
	if waiting {
		g.Files, g.Of = set.Files, set.Of
	}
	return g
}

// numericLess compares names as a person reads them: digits by value, the rest
// without regard to case.
func numericLess(a, b string) bool {
	ra, rb := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	i, j := 0, 0
	for i < len(ra) && j < len(rb) {
		if unicode.IsDigit(ra[i]) && unicode.IsDigit(rb[j]) {
			si, sj := i, j
			for i < len(ra) && unicode.IsDigit(ra[i]) {
				i++
			}
			for j < len(rb) && unicode.IsDigit(rb[j]) {
				j++
			}
			na, nb := strings.TrimLeft(string(ra[si:i]), "0"), strings.TrimLeft(string(rb[sj:j]), "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			continue
		}
		if ra[i] != rb[j] {
			return ra[i] < rb[j]
		}
		i++
		j++
	}
	return len(ra)-i < len(rb)-j
}
