package archive

import (
	"regexp"
	"strconv"
	"strings"
)

// LastPart is the part number of a split ZIP's .zip, which comes after its
// .z01, .z02, …: its number is only known once the others are.
const LastPart = -1

var partNames = []struct {
	re   *regexp.Regexp
	ext  string // added to the base to name the set; "" when the base has it
	part func(n int) int
}{
	{regexp.MustCompile(`(?i)^(.+)\.part0*(\d+)\.rar$`), ".rar", func(n int) int { return n }},
	{regexp.MustCompile(`(?i)^(.+)\.r(\d{2,3})$`), ".rar", func(n int) int { return n + 2 }}, // .rar is part 1, .r00 part 2
	{regexp.MustCompile(`(?i)^(.+)\.rar()$`), ".rar", func(int) int { return 1 }},
	{regexp.MustCompile(`(?i)^(.+\.(?:7z|zip|rar))\.(\d{3})$`), "", func(n int) int { return n }},
	{regexp.MustCompile(`(?i)^(.+)\.z(\d{2,3})$`), ".zip", func(n int) int { return n }},
	{regexp.MustCompile(`(?i)^(.+)\.zip()$`), ".zip", func(int) int { return LastPart }},
}

// PartOf says whether a file name may be one part of an archive split into
// several (movie.part2.rar, movie.r00, movie.7z.001, movie.z01): the set's
// name (movie.rar, movie.7z, movie.zip) and the part's number. A plain
// movie.rar or movie.zip names a set too, of which it may be the only part.
func PartOf(name string) (set string, part int, ok bool) {
	if !mayBePart(name) {
		return "", 0, false
	}
	for _, p := range partNames {
		m := p.re.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		if p.ext == "" {
			return m[1], p.part(n), true
		}
		return m[1] + p.ext, p.part(n), true
	}
	return "", 0, false
}

// mayBePart is a cheap test every name PartOf accepts passes: the last
// extension is rar or zip, or three digits, or r or z and two or three digits.
// It keeps the six regexes off ordinary names, which are most of a long list.
func mayBePart(name string) bool {
	ext := name[strings.LastIndexByte(name, '.')+1:]
	if strings.EqualFold(ext, "rar") || strings.EqualFold(ext, "zip") {
		return true
	}
	if len(ext) == 3 && isDigits(ext) {
		return true
	}
	return (len(ext) == 3 || len(ext) == 4) && strings.ContainsRune("rRzZ", rune(ext[0])) && isDigits(ext[1:])
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// SetKey is the same for the parts of one set saved in one folder, ignoring
// case as Windows does.
func SetKey(dir, set string) string {
	return "parts:" + strings.ToLower(dir) + "|" + strings.ToLower(set)
}
