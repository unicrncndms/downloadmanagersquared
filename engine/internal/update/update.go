// Package update asks GitHub, at most once a day, whether a newer release of
// the engine exists, and on the user's request downloads it: the release zip
// over HTTPS from github.com, checked against the SHA-256 GitHub lists for the
// asset, of which only dms-engine.exe is taken out. Installing it is the
// normal upgrade path (starting that exe). Never automatic.
package update

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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

// Load reads the saved state; a missing or broken file, or a release Fetch
// would not have accepted, is a zero State.
func Load(path string) State {
	var s State
	if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &s) == nil && s.valid() {
		return s
	}
	return State{}
}

// valid is whether a saved release is one Fetch would have accepted.
func (s State) valid() bool {
	if s.Latest == "" && s.URL == "" {
		return true
	}
	return Release(s.Latest) && strings.HasPrefix(s.URL, "https://github.com/")
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

// Zip is the release asset that carries the engine.
const Zip = "downloadmanagersquared-windows.zip"

// maxZip caps the download and the exe taken out of it.
const maxZip = 200 << 20

// assetPrefix is where a download may come from (tests replace it).
var assetPrefix = "https://github.com/"

// Latest is what GitHub says about the newest release.
type Latest struct {
	Version string // without the "v"
	URL     string // the release page
	ZipURL  string // the engine zip, "" when the release has none
	Digest  string // "sha256:<hex>" of the zip, "" when GitHub gave none
}

// Fetch asks GitHub's API (apiURL: .../repos/<owner>/<repo>/releases/latest)
// for the latest release.
func Fetch(ctx context.Context, client *http.Client, apiURL string) (version, url string, err error) {
	l, err := FetchLatest(ctx, client, apiURL)
	return l.Version, l.URL, err
}

// FetchLatest is Fetch with the engine zip's address and digest.
func FetchLatest(ctx context.Context, client *http.Client, apiURL string) (Latest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return Latest{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "DMS2-engine")
	resp, err := client.Do(req)
	if err != nil {
		return Latest{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Latest{}, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var r struct {
		Tag    string `json:"tag_name"`
		URL    string `json:"html_url"`
		Assets []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return Latest{}, err
	}
	v := strings.TrimPrefix(r.Tag, "v")
	if _, ok := parse(v); !ok {
		return Latest{}, fmt.Errorf("the latest release's tag %q is not a version", r.Tag)
	}
	if !strings.HasPrefix(r.URL, "https://github.com/") {
		return Latest{}, fmt.Errorf("the latest release's page %q is not on GitHub", r.URL)
	}
	l := Latest{Version: v, URL: r.URL}
	for _, a := range r.Assets {
		if a.Name == Zip {
			l.ZipURL, l.Digest = a.URL, a.Digest
		}
	}
	return l, nil
}

// Download fetches the zip at assetURL, checks it against digest
// ("sha256:<hex>"), and writes the zip's entry named exactly dms-engine.exe
// into a new temp folder, returning that exe's path. Nothing else of the
// archive is extracted, and no name in it is used as a path.
func Download(ctx context.Context, client *http.Client, assetURL, digest string) (string, error) {
	if !strings.HasPrefix(assetURL, assetPrefix) {
		return "", fmt.Errorf("the update's address %q is not on GitHub", assetURL)
	}
	want, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || len(want) != 64 {
		return "", errors.New("GitHub gave no checksum for this release, so it is not installed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "DMS2-engine")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub answered %s", resp.Status)
	}
	dir, err := os.MkdirTemp("", "dms-update-")
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) { os.RemoveAll(dir); return "", err }
	zipPath := filepath.Join(dir, "update.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		return fail(err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxZip+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fail(err)
	}
	if n > maxZip {
		return fail(errors.New("the update is larger than expected, so it is not installed"))
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want) {
		return fail(errors.New("the downloaded update does not match its checksum, so it is not installed"))
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fail(err)
	}
	defer zr.Close()
	for _, zf := range zr.File {
		if zf.Name != "dms-engine.exe" {
			continue
		}
		in, err := zf.Open()
		if err != nil {
			return fail(err)
		}
		exe := filepath.Join(dir, "dms-engine.exe")
		out, err := os.Create(exe)
		if err == nil {
			var c int64
			c, err = io.Copy(out, io.LimitReader(in, 2*maxZip+1))
			if err == nil && c > 2*maxZip {
				err = errors.New("the update's engine is larger than expected, so it is not installed")
			}
			if cerr := out.Close(); err == nil {
				err = cerr
			}
		}
		in.Close()
		if err != nil {
			return fail(err)
		}
		return exe, nil
	}
	return fail(errors.New("the update has no dms-engine.exe, so it is not installed"))
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
