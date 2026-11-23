package engine

import (
	"cmp"
	"errors"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// A question the engine holds a new download back for (a file of that name is
// already there). The pages show it on the downloads list; the answer adds
// the download one way or drops it. Held questions are not saved: after a
// restart the link is simply not added.

// The onExists setting's values: what to do with a download whose file is already there.
const (
	OnExistsRename    = "rename"    // keep both: the new one gets a number (name (1).ext)
	OnExistsOverwrite = "overwrite" // replace it, after a warning at add time
	OnExistsSkip      = "skip"      // don't add it
	OnExistsAsk       = "ask"       // ask each time
)

// AskChoice is one answer to an AskView; Key goes back in answerAsk.
type AskChoice struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// AskView is a held question as the pages show it.
type AskView struct {
	ID       string      `json:"id"`
	Text     string      `json:"text"`
	Choices  []AskChoice `json:"choices"`
	Existing string      `json:"existing,omitempty"` // a download in the list the question is about
}

type heldAdd struct {
	view AskView
	req  AddRequest
}

// maxHeld is how many questions wait at most: an older one is dropped.
const maxHeld = 20

// plainLink: r adds one http(s) file, which can be named and checked against
// the disk before it is added (not a torrent, a Metalink or a local file).
func plainLink(r AddRequest) bool {
	u := strings.TrimSpace(r.URL)
	return r.File == "" && r.FilePath == "" && !r.Torrent && !r.Metalink && !isTorrentURL(u) && !isMetalinkURL(u) &&
		(strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://"))
}

// addAsking is what the add method calls: it adds r unless the user must
// first say what to do about its file already being there. The reply is
// {"id"} when added, {"ask"} when held for an answer, {"skipped"} (with the
// reason) when the settings say not to add it.
func (e *Engine) addAsking(r AddRequest) (any, error) {
	r.URL = strings.TrimSpace(r.URL)
	if r.CheckDup && r.Ask && r.File == "" && r.FilePath == "" && r.Answered != "again" {
		if id := e.findDup(r); id != "" {
			return e.hold(r, e.dupText(id), id, []AskChoice{{"again", "Add anyway"}, {"show", "Open it"}, {"cancel", "Cancel"}}), nil
		}
	}
	if plainLink(r) && r.Answered != "rename" && r.Answered != "replace" {
		e.mu.Lock()
		name := nameOf(r)
		dir := cmp.Or(r.Dir, e.settings.ruleDir(r.URL, r.PageURL, name), e.settings.SaveDir)
		onExists := e.settings.OnExists
		e.mu.Unlock()
		if _, err := os.Lstat(filepath.Join(dir, name)); name != "" && err == nil {
			here := `"` + name + `" is already in ` + dir + "."
			switch onExists {
			case OnExistsSkip:
				return map[string]string{"skipped": "Not added: " + here}, nil
			case OnExistsOverwrite:
				return e.hold(r, here+" Downloading it again replaces that file.", "", []AskChoice{{"replace", "Replace it"}, {"cancel", "Cancel"}}), nil
			case OnExistsAsk:
				return e.hold(r, here, "", []AskChoice{{"rename", "Keep both"}, {"replace", "Replace it"}, {"cancel", "Skip"}}), nil
			}
		}
	}
	id, err := e.Add(r)
	return map[string]string{"id": id}, err
}

// dupText says that r is already in the list as download id, and how it is doing.
func (e *Engine) dupText(id string) string {
	what, how := "This link", ""
	if d, err := e.queue.Get(id); err == nil {
		s := d.Summary()
		how = " (" + s.Label + ")"
		if name := cmp.Or(s.Filename, urlName(s.URL)); name != "" { // not probed yet: the link's own name
			what = `"` + name + `"`
		}
	}
	return what + " is already in the list" + how + "."
}

// urlName is the last part of a link's path ("f.iso"), or "" if it has none.
func urlName(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	name := path.Base(u.Path)
	if name == "." || name == "/" {
		return ""
	}
	return name
}

// hold keeps r back until its question is answered.
func (e *Engine) hold(r AddRequest, text, existing string, choices []AskChoice) map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	h := &heldAdd{view: AskView{ID: newID(), Text: text, Choices: choices, Existing: existing}, req: r}
	e.held = append(e.held, h)
	if len(e.held) > maxHeld {
		e.held = e.held[1:]
	}
	return map[string]string{"ask": h.view.ID}
}

// answerAsk settles a held question with one of its choices. "rename",
// "replace" and "again" add the download that way; any other choice drops it.
func (e *Engine) answerAsk(id, choice string) (any, error) {
	e.mu.Lock()
	i := slices.IndexFunc(e.held, func(h *heldAdd) bool { return h.view.ID == id })
	if i < 0 {
		e.mu.Unlock()
		return nil, errors.New("that question was already answered")
	}
	h := e.held[i]
	if !slices.ContainsFunc(h.view.Choices, func(c AskChoice) bool { return c.Key == choice }) {
		e.mu.Unlock()
		return nil, errors.New("not an answer to that question")
	}
	e.held = slices.Delete(e.held, i, i+1)
	e.mu.Unlock()
	switch choice {
	case "rename", "replace", "again":
		h.req.Answered = choice
		return e.addAsking(h.req)
	}
	return nil, nil
}

// asksLocked are the held questions, for the state sent to the pages.
func (e *Engine) asksLocked() []AskView {
	var out []AskView
	for _, h := range e.held {
		out = append(out, h.view)
	}
	return out
}
