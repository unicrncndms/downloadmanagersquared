package download

import "testing"

func TestPartsOfASplitArchiveShareAGroup(t *testing.T) {
	sum := func(dir, name, group string) Summary {
		return New(nil, State{ID: name, URL: "https://x.example/" + name, Dir: dir, Filename: name, Group: group, Size: -1}).Summary()
	}
	a, b := sum(`C:\D`, "film.part1.rar", ""), sum(`C:\D`, "film.part2.rar", "")
	if a.Group == "" || a.Group != b.Group || a.GroupName != "film.rar" {
		t.Fatalf("parts grouped as %q/%q and %q", a.Group, a.GroupName, b.Group)
	}
	if other := sum(`C:\E`, "film.part3.rar", ""); other.Group == a.Group {
		t.Fatal("a part in another folder joined the set")
	}
	if m := sum(`C:\D`, "film.part4.rar", "metalink1"); m.Group != "metalink1" {
		t.Fatalf("a Metalink's group became %q", m.Group)
	}
	if p := sum(`C:\D`, "film.mkv", ""); p.Group != "" {
		t.Fatalf("a plain file got group %q", p.Group)
	}
}
