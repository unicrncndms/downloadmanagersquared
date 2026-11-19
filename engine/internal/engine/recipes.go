package engine

import (
	"encoding/json"
	"log"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"downloadmanagersquared/engine/internal/atomicfile"
	"downloadmanagersquared/engine/internal/ipc"
	"downloadmanagersquared/engine/internal/resolver"
	"downloadmanagersquared/engine/internal/winutil"
)

// The recipes of every browser, kept by the engine (ticket 37): the engine's
// browser and Firefox's resolver record into one store and replay from it,
// and every settings page and the Downloads window list it. Each change goes
// to every client as a "recipes" event.

// openRecipes reads dir's recipes.json; one that can't be read is set aside as
// recipes.json.broken (as settings.json is) and the store starts empty.
func openRecipes(dir string) *resolver.Recipes {
	path := filepath.Join(dir, "recipes.json")
	r, err := resolver.OpenRecipes(path)
	if err != nil {
		log.Printf("the recipes can't be read (%v): kept as recipes.json.broken, starting without them", err)
		os.Rename(path, path+".broken")
		r, _ = resolver.OpenRecipes(path)
	}
	return r
}

type recipeParams struct {
	Host   string           `json:"host"`
	Recipe *resolver.Recipe `json:"recipe"` // nil forgets the host's
}

type mergeRecipesParams struct {
	Recipes map[string]*resolver.Recipe `json:"recipes"`
}

// recipesChanged tells every client the recipes now.
func (e *Engine) recipesChanged() { e.broadcast("recipes", e.recipes.All()) }

// setRecipe keeps a recipe a browser's resolver recorded (or measured its
// waits on), marked with that browser.
func (e *Engine) setRecipe(c *ipc.Conn, p recipeParams) error {
	if p.Recipe != nil && p.Recipe.From == "" {
		p.Recipe.From = e.browserNameOf(c)
	}
	if err := e.recipes.Set(p.Host, p.Recipe); err != nil {
		return err
	}
	e.recipesChanged()
	return nil
}

// mergeRecipes takes a browser's own recipes from before they were kept here:
// per host, the newer one stays.
func (e *Engine) mergeRecipes(c *ipc.Conn, p mergeRecipesParams) error {
	from := e.browserNameOf(c)
	changed := false
	for host, rec := range p.Recipes {
		if rec == nil || host == "" {
			continue
		}
		if have := e.recipes.Get(host); have != nil && have.Learned >= rec.Learned {
			continue
		}
		if rec.From == "" {
			rec.From = from
		}
		if err := e.recipes.Set(host, rec); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		e.recipesChanged()
	}
	return nil
}

// browserNameOf is the name of the browser client c's extension runs in.
func (e *Engine) browserNameOf(c *ipc.Conn) string {
	e.mu.Lock()
	exe := ""
	if cl := e.clients[c]; cl != nil {
		exe = cl.browser
	}
	e.mu.Unlock()
	return browserName(exe)
}

// browserName is how Windows lists the browser at exe ("Google Chrome"), else
// its file name without .exe.
func browserName(exe string) string {
	if exe == "" {
		return ""
	}
	for _, b := range winutil.Browsers() {
		if strings.EqualFold(b.Exe, exe) {
			return b.Name
		}
	}
	return strings.TrimSuffix(filepath.Base(exe), filepath.Ext(exe))
}

// Step-backs (host -> how many pages before a download its landing page is)
// are learned by any browser's recording and shared like the recipes (ticket
// 36): a site learned in Firefox starts on the right page in Chrome too.
type stepBacks struct {
	mu   sync.Mutex
	path string
	m    map[string]int
}

type stepBacksParams struct {
	StepBacks map[string]int `json:"stepBacks"`
	Set       bool           `json:"set"` // overwrite; else only hosts not known yet are added
}

func openStepBacks(dir string) *stepBacks {
	s := &stepBacks{path: filepath.Join(dir, "stepbacks.json"), m: map[string]int{}}
	if b, err := os.ReadFile(s.path); err == nil && json.Unmarshal(b, &s.m) != nil {
		log.Printf("stepbacks.json can't be read: starting without it")
		s.m = map[string]int{}
	}
	return s
}

// add merges p in, saves, and answers all of them; a change goes to every client.
func (e *Engine) addStepBacks(p stepBacksParams) (map[string]int, error) {
	s := e.stepBacks
	s.mu.Lock()
	changed := false
	for host, n := range p.StepBacks {
		if host == "" || n < 0 {
			continue
		}
		if have, ok := s.m[host]; ok && (have == n || !p.Set) {
			continue
		}
		s.m[host], changed = n, true
	}
	all := maps.Clone(s.m)
	var err error
	if changed {
		err = atomicfile.WriteJSON(s.path, all)
	}
	s.mu.Unlock()
	if changed {
		e.broadcast("stepBacks", all)
	}
	return all, err
}
