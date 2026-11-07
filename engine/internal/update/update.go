// Package update asks GitHub, at most once a day, whether a newer release of
// the engine exists. It only tells: nothing is downloaded or installed, since
// the binaries aren't code-signed and the install folder may need
// administrator rights.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"downloadmanagersquared/engine/internal/atomicfile"
)

// Every is how often GitHub is asked.
const Every = 24 * time.Hour

// State is what the last check found, saved so a restart doesn't ask again.
type State struct {
	Checked time.Time `json:"checked"`
	Latest  string    `json:"latest,omitempty"` // the newest release's version, without the "v"
	URL     string    `json:"url,omitempty"`    // its release page
}

// Load reads the saved state; a missing or broken file is a zero State.
func Load(path string) State {
	var s State
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// Save writes the state.
func (s State) Save(path string) error { return atomicfile.WriteJSON(path, s) }

// Due reports whether a day has passed since the last check (or the clock
// went back).
func (s State) Due(now time.Time) bool {
	return s.Checked.IsZero() || now.Sub(s.Checked) >= Every || now.Before(s.Checked)
}

// Available is the newer release for current, if any: its version and page.
// A development build ("dev", or anything not X.Y.Z) never has one.
func (s State) Available(current string) (version, url string, ok bool) {
	if s.Latest == "" || !Newer(s.Latest, current) {
		return "", "", false
	}
	return s.Latest, s.URL, true
}

// Fetch asks GitHub's API (apiURL: .../repos/<owner>/<repo>/releases/latest)
// for the latest release.
func Fetch(ctx context.Context, client *http.Client, apiURL string) (version, url string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "DMS2-engine")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var r struct {
		Tag string `json:"tag_name"`
		URL string `json:"html_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return "", "", err
	}
	v := strings.TrimPrefix(r.Tag, "v")
	if _, ok := parse(v); !ok {
		return "", "", fmt.Errorf("the latest release's tag %q is not a version", r.Tag)
	}
	if !strings.HasPrefix(r.URL, "https://github.com/") {
		return "", "", fmt.Errorf("the latest release's page %q is not on GitHub", r.URL)
	}
	return v, r.URL, nil
}

// Release reports whether v is a release version (X.Y.Z), not a development
// build: only releases ask GitHub.
func Release(v string) bool {
	_, ok := parse(v)
	return ok
}

// Newer reports whether version a is newer than b, both X.Y.Z. Anything else
// (a development build) is never newer nor older.
func Newer(a, b string) bool {
	x, ok1 := parse(a)
	y, ok2 := parse(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
