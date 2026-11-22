package engine

import "testing"

// A link already in the list is held for a question, whatever its token
// parameters say; for a resolved link the landing page and file name decide.
// "Add anyway" adds it, the other answers don't.
func TestARepeatedLinkIsAskedAbout(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	e.settings.MaxParallel = 0
	add := func(link, page, name string) map[string]string {
		t.Helper()
		r, err := e.addAsking(AddRequest{URL: link, PageURL: page, Filename: name, CheckDup: true, Ask: true, Dir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		return r.(map[string]string)
	}
	listed := func() int { return len(e.queue.List()) }

	first := add("https://cdn.example/f.iso?token=aaa&expires=1", "", "")["id"]
	if first == "" || listed() != 1 {
		t.Fatal("the first link was not added")
	}
	r := add("https://cdn.example/f.iso?expires=2&token=bbb", "", "")
	if r["ask"] == "" || listed() != 1 {
		t.Fatalf("same link, new token: %v", r)
	}
	e.mu.Lock()
	view := e.asksLocked()[0]
	e.mu.Unlock()
	if view.Existing != first || len(view.Choices) != 3 {
		t.Fatalf("question %+v", view)
	}
	if _, err := e.answerAsk(r["ask"], "show"); err != nil || listed() != 1 {
		t.Fatalf("show added it: %v", err)
	}
	r = add("https://cdn.example/f.iso?token=ccc", "", "")
	if _, err := e.answerAsk(r["ask"], "again"); err != nil || listed() != 2 {
		t.Fatalf("add anyway: %v, listed %d", err, listed())
	}

	// A host that gives a new link every time: its landing page and the file name are the same.
	e.settings.OnExists = OnExistsRename
	one := add("https://dl1.host.example/a8f3/Setup.exe", "https://host.example/get/42?session=x1", "Setup.exe")["id"]
	if one == "" {
		t.Fatal("not added")
	}
	if r := add("https://dl2.host.example/b7c4/Setup.exe", "https://host.example/get/42?session=y2", "setup.exe"); r["ask"] == "" {
		t.Fatalf("same landing page and name: %v", r)
	}
	if r := add("https://dl2.host.example/b7c4/Other.exe", "https://host.example/get/42", "Other.exe"); r["id"] == "" {
		t.Fatalf("another file from the same page: %v", r)
	}
}
