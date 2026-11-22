package engine

import "testing"

func TestFolderRules(t *testing.T) {
	rules := cleanFolderRules([]FolderRule{
		{Match: " *.ISO ", Dir: `D:\ISOs`},
		{Match: "https://www.Files.Example/x", Dir: `E:\Files`},
		{Match: ".zip", Dir: "relative"}, // not a folder: dropped
		{Match: "", Dir: `D:\x`},         // no match: dropped
	})
	if len(rules) != 2 || rules[0].Match != ".iso" || rules[1].Match != "files.example" {
		t.Fatalf("cleaned: %+v", rules)
	}
	s := Settings{FolderRules: rules}
	for _, c := range []struct{ link, page, name, want string }{
		{"https://a.test/x", "", "Debian.ISO", `D:\ISOs`},
		{"https://cdn.files.example/x", "", "a.zip", `E:\Files`},
		{"https://a.test/x", "https://files.example/get", "a.zip", `E:\Files`},
		{"https://notfiles.example/x", "", "a.zip", ""},
		{"https://a.test/x.iso", "", "", ""}, // no name known: the extension isn't guessed
	} {
		if got := s.ruleDir(c.link, c.page, c.name); got != c.want {
			t.Errorf("%+v: got %q", c, got)
		}
	}
}

func TestAddUsesFolderRuleUnlessAFolderIsGiven(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	e.settings.MaxParallel = 0
	e.settings.FolderRules = cleanFolderRules([]FolderRule{{Match: ".iso", Dir: `D:\ISOs`}})
	ruled, _ := e.Add(AddRequest{URL: "https://example.com/big.iso"})
	given, _ := e.Add(AddRequest{URL: "https://example.com/big.iso", Dir: `E:\Mine`})
	other, _ := e.Add(AddRequest{URL: "https://example.com/big.zip"})
	if got := summaryOf(t, e, ruled).Dir; got != `D:\ISOs` {
		t.Errorf("rule: %q", got)
	}
	if got := summaryOf(t, e, given).Dir; got != `E:\Mine` {
		t.Errorf("given: %q", got)
	}
	if got := summaryOf(t, e, other).Dir; got != e.settings.SaveDir {
		t.Errorf("no rule: %q", got)
	}
}
