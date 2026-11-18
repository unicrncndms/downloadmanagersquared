package resolver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"downloadmanagersquared/engine/internal/cdp"
	"downloadmanagersquared/engine/internal/ui"
)

// The page script is the extension's own: lib/ready.js and resolver-content.js,
// carried in ui.Pages (build.ps1). It runs in an isolated world, so the page
// sees neither it nor the binding; the prelude gives it the two browser.runtime
// calls it uses, over that binding.

const (
	world   = "DMS² resolver"
	binding = "__dmsSend"
)

const prelude = `(() => {
  const listeners = [];
  globalThis.browser = { runtime: {
    sendMessage: (m) => { globalThis.` + binding + `(JSON.stringify(m)); return Promise.resolve(); },
    onMessage: { addListener: (f) => { listeners.push(f); } },
  } };
  globalThis.__dmsDeliver = (m) => { for (const f of listeners) f(m); };
})();
`

// pageScript is the prelude and the two files; a var for tests.
var pageScript = func() (string, error) {
	ready, err := fs.ReadFile(ui.Pages, "lib/ready.js")
	if err != nil {
		return "", errors.New("this engine was built without its resolver page script (build.ps1 adds it)")
	}
	content, err := fs.ReadFile(ui.Pages, "resolver-content.js")
	if err != nil {
		return "", errors.New("this engine was built without its resolver page script (build.ps1 adds it)")
	}
	return prelude + string(ready) + "\n" + string(content), nil
}

func (d *Driver) bindingCalled(e cdp.Event) {
	var p struct {
		Name    string `json:"name"`
		Payload string `json:"payload"`
	}
	json.Unmarshal(e.Params, &p)
	if p.Name != binding {
		return
	}
	d.mu.Lock()
	page := d.sessions[e.SessionID]
	d.mu.Unlock()
	if page != "" && d.ev.Message != nil {
		d.ev.Message(page, p.Payload)
	}
}

// Drive puts the page script into target's current document, once per
// document, like the extension's drive().
func (d *Driver) Drive(target string) error {
	s := d.session(target)
	if s == "" {
		return fmt.Errorf("page %s is not attached", target)
	}
	d.mu.Lock()
	ctx, ok := d.worlds[target]
	d.mu.Unlock()
	if ok {
		if _, err := d.evaluate(s, ctx, "1"); err == nil {
			return nil // that document still has it
		}
	}
	src, err := pageScript()
	if err != nil {
		return err
	}
	r, err := d.call(s, "Page.getFrameTree", nil)
	if err != nil {
		return err
	}
	var tree struct {
		FrameTree struct {
			Frame struct{ ID string } `json:"frame"`
		} `json:"frameTree"`
	}
	json.Unmarshal(r, &tree)
	r, err = d.call(s, "Page.createIsolatedWorld", map[string]any{"frameId": tree.FrameTree.Frame.ID, "worldName": world})
	if err != nil {
		return err
	}
	var w struct {
		ExecutionContextID int `json:"executionContextId"`
	}
	json.Unmarshal(r, &w)
	// Only the world of that name can call it, and only one that exists when it
	// is added (checked on Edge 157). Runtime is not enabled: sites can detect that.
	if _, err := d.call(s, "Runtime.addBinding", map[string]any{"name": binding, "executionContextName": world}); err != nil {
		return err
	}
	if _, err := d.evaluate(s, w.ExecutionContextID, src); err != nil {
		return err
	}
	d.mu.Lock()
	d.worlds[target] = w.ExecutionContextID
	d.mu.Unlock()
	return nil
}

// Send hands msg to the page script's onMessage listeners, as tabs.sendMessage.
func (d *Driver) Send(target string, msg any) error {
	s := d.session(target)
	d.mu.Lock()
	ctx, ok := d.worlds[target]
	d.mu.Unlock()
	if s == "" || !ok {
		return fmt.Errorf("page %s has no page script", target)
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = d.evaluate(s, ctx, "__dmsDeliver("+string(b)+")")
	return err
}

// evaluate runs js in a context and fails on a thrown exception too.
func (d *Driver) evaluate(session string, ctx int, js string) (json.RawMessage, error) {
	r, err := d.call(session, "Runtime.evaluate", map[string]any{"expression": js, "contextId": ctx, "returnByValue": true})
	if err != nil {
		return nil, err
	}
	var res struct {
		Exception *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	json.Unmarshal(r, &res)
	if x := res.Exception; x != nil {
		return nil, errors.New("page script: " + strings.TrimSpace(x.Text+" "+x.Exception.Description))
	}
	return r, nil
}
