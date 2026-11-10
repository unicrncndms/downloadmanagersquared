package archive

import "testing"

func TestPartOf(t *testing.T) {
	for _, c := range []struct {
		name, set string
		part      int
		ok        bool
	}{
		{"Movie.part1.rar", "Movie.rar", 1, true},
		{"Movie.part02.rar", "Movie.rar", 2, true},
		{"movie.rar", "movie.rar", 1, true},
		{"movie.r00", "movie.rar", 2, true},
		{"movie.R01", "movie.rar", 3, true},
		{"movie.7z.001", "movie.7z", 1, true},
		{"movie.7z.012", "movie.7z", 12, true},
		{"movie.zip.003", "movie.zip", 3, true},
		{"movie.rar.002", "movie.rar", 2, true},
		{"movie.z01", "movie.zip", 1, true},
		{"movie.zip", "movie.zip", LastPart, true},
		{"movie.7z", "", 0, false},
		{"movie.mkv", "", 0, false},
		{"movie.001", "", 0, false},
		{"report.part1.pdf", "", 0, false},
	} {
		set, part, ok := PartOf(c.name)
		if set != c.set || part != c.part || ok != c.ok {
			t.Errorf("PartOf(%q) = %q, %d, %v; want %q, %d, %v", c.name, set, part, ok, c.set, c.part, c.ok)
		}
	}
}

func TestSetKeyIgnoresCase(t *testing.T) {
	if SetKey(`C:\Downloads`, "Movie.rar") != SetKey(`c:\downloads`, "movie.RAR") {
		t.Fatal("keys differ by case")
	}
	if SetKey(`C:\A`, "movie.rar") == SetKey(`C:\B`, "movie.rar") {
		t.Fatal("different folders share a key")
	}
}
