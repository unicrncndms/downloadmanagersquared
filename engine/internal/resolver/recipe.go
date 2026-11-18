package resolver

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"downloadmanagersquared/engine/internal/atomicfile"
)

// The rules of extension/lib/recipe.js, route.js and ready.js the loop needs.
// The Firefox resolver keeps its own copy; recipe_test.go has the extension's
// cases, so the two stay in step.

// Step is one remembered click, as resolver-content.js describes it, and the
// wait before it in ms (from the page load or the previous click).
type Step struct {
	Tag      string `json:"tag"`
	ID       string `json:"id"`
	Text     string `json:"text"`
	Selector string `json:"selector"`
	Wait     int64  `json:"wait"`
}

// Recipe is how to get a host's download link: the clicks, and the wait from
// the last one to the download.
type Recipe struct {
	Steps        []Step `json:"steps"`
	DownloadWait int64  `json:"downloadWait"`
	Learned      int64  `json:"learned"` // ms since 1970
}

const (
	budgetSlack = 15 * time.Second
	replaySlack = time.Minute
	recordIdle  = 5 * time.Minute // without a click, page load or captcha change
	maxWindows  = 2               // per page host (ADR 0010)

	effectWait = 2 * time.Second // a click has to show an effect
	maxClicks  = 5               // per step, before asking for help
	popupGrace = 4 * time.Second // a new tab on another site has to start a download
)

func ms(n int64) time.Duration { return time.Duration(n) * time.Millisecond }

// budget: how long a step may take at most, twice its wait plus 15 s.
func budget(wait int64) time.Duration { return 2*ms(wait) + budgetSlack }

// replayTimeout: how long a whole replay may take.
func replayTimeout(r *Recipe) time.Duration {
	if r == nil {
		return budget(0) + replaySlack
	}
	sum := budget(r.DownloadWait) + replaySlack
	for _, s := range r.Steps {
		sum += budget(s.Wait)
	}
	return sum
}

// recorded is the recipe a recording leaves: the old recipe's first
// recordFrom steps, then the recorded ones.
func recorded(old *Recipe, recordFrom int, steps []Step, downloadWait int64, now time.Time) *Recipe {
	var kept []Step
	if old != nil {
		kept = old.Steps[:min(recordFrom, len(old.Steps))]
	}
	return &Recipe{Steps: append(append([]Step{}, kept...), steps...), DownloadWait: downloadWait, Learned: now.UnixMilli()}
}

// withMeasuredWaits: after a successful replay, the waits seen this time
// replace the recorded ones (a step not in measured was skipped).
func withMeasuredWaits(r *Recipe, steps map[int]int64, download int64, haveDownload bool) *Recipe {
	out := *r
	out.Steps = append([]Step{}, r.Steps...)
	for i := range out.Steps {
		if w, ok := steps[i]; ok {
			out.Steps[i].Wait = w
		}
	}
	if haveDownload {
		out.DownloadWait = download
	}
	return &out
}

// afterClick: what to do some time after the clicks-th click of a step, given
// the effects seen since. "done", "wait", "retry" or "giveUp".
func afterClick(effects map[string]bool, since time.Duration, clicks int) string {
	if effects["navigation"] || effects["newTab"] || effects["download"] || effects["nextStep"] {
		return "done"
	}
	if since < effectWait {
		return "wait"
	}
	if clicks < maxClicks {
		return "retry"
	}
	return "giveUp"
}

// popupVerdict: a tab a click opened is kept on the same site or when it
// started a download, closed when on another site past the grace, else waited on.
func popupVerdict(openerSite, site string, age time.Duration, download bool) string {
	if download || site == openerSite {
		return "keep"
	}
	if age >= popupGrace {
		return "close"
	}
	return "wait"
}

func hostOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return p.Hostname()
}

// siteOf: rough "same site", the last two labels of the host.
func siteOf(u string) string {
	labels := strings.Split(hostOf(u), ".")
	return strings.Join(labels[max(0, len(labels)-2):], ".")
}

func isHTTP(u string) bool { return strings.HasPrefix(u, "http:") || strings.HasPrefix(u, "https:") }

// Recipes are the recipes recorded in the engine's browser, by landing-page
// host, kept in one file. Safe for concurrent use.
type Recipes struct {
	path string
	mu   sync.Mutex
	by   map[string]*Recipe
}

// OpenRecipes reads path; a missing file is no recipes.
func OpenRecipes(path string) (*Recipes, error) {
	r := &Recipes{path: path, by: map[string]*Recipe{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &r.by); err != nil {
		return nil, err
	}
	return r, nil
}

// Get is host's recipe, nil when there is none. The caller must not change it.
func (r *Recipes) Get(host string) *Recipe {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.by[host]
}

// Set keeps host's recipe, nil removes it.
func (r *Recipes) Set(host string, rec *Recipe) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec == nil {
		delete(r.by, host)
	} else {
		r.by[host] = rec
	}
	if r.path == "" {
		return nil
	}
	return atomicfile.WriteJSON(r.path, r.by)
}

// Hosts are the hosts with a recipe, sorted.
func (r *Recipes) Hosts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.by))
	for h := range r.by {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}
