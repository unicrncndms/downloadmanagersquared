package engine

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/metalink"
	"downloadmanagersquared/engine/internal/paths"
	"downloadmanagersquared/engine/internal/torrent"
)

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type AddRequest struct {
	URL       string `json:"url"`
	PageURL   string `json:"pageUrl"`
	Filename  string `json:"filename"`
	Cookie    string `json:"cookie"`
	Referer   string `json:"referer"`
	UserAgent string `json:"userAgent"`
	Torrent   bool   `json:"torrent"`  // the URL serves a .torrent file
	Metalink  bool   `json:"metalink"` // the URL serves a Metalink (.meta4)
	VPN       string `json:"vpn"`      // "off", "alongside" or "vpn"; "" = the default for its type
	Folder    *bool  `json:"folder"`   // Download as folder: on (asks for the folder), off, or nil for the default
	Browser   string `json:"-"`        // the exe of the browser that sent it, known from its connection
	// CookieStore is the browser's cookie store (container) the download came from.
	CookieStore string `json:"cookieStore"`
}

func (e *Engine) Add(r AddRequest) (string, error) {
	r.URL = strings.TrimSpace(r.URL)
	if strings.HasPrefix(r.URL, "magnet:") {
		id, err := e.addTorrent(torrent.State{Magnet: r.URL}, r.VPN)
		if err != nil {
			return "", fmt.Errorf("not a usable magnet link: %w", err)
		}
		return id, nil
	}
	if !strings.HasPrefix(r.URL, "http://") && !strings.HasPrefix(r.URL, "https://") {
		return "", errors.New("only http, https and magnet links are supported")
	}
	if r.UserAgent == "" {
		// Some servers drop clients with Go's default user agent: use the browser's.
		e.mu.Lock()
		r.UserAgent = e.userAgent
		e.mu.Unlock()
	}
	if r.UserAgent == "" {
		r.UserAgent = defaultUserAgent
	}
	if r.Torrent || isTorrentURL(r.URL) {
		return e.addTorrentURL(r)
	}
	if r.Metalink || isMetalinkURL(r.URL) {
		return e.addMetalinkURL(r)
	}
	e.mu.Lock()
	st := download.State{
		VPN: paths.ParseMode(r.VPN, e.settings.VPNHTTP),
		ID:  newID(), URL: r.URL, PageURL: strings.TrimSpace(r.PageURL),
		Cookie: download.Secret(r.Cookie), Referer: r.Referer, UserAgent: r.UserAgent, Browser: r.Browser,
		CookieStore: r.CookieStore, Dir: e.settings.SaveDir, Filename: download.SanitizeFilename(r.Filename),
		Size: -1, Status: download.StatusQueued, Added: time.Now(),
	}
	switch {
	case r.Folder != nil && *r.Folder:
		// Switched on by hand: it asks where to extract (settled once the name
		// is known: only a .zip stays one).
		st.Folder = &download.Folder{On: true}
	case r.Folder == nil && e.settings.FolderDefault:
		// Settled once the name is known: only a .zip becomes one.
		st.Folder = &download.Folder{Auto: true, Into: e.settings.folderDir()}
	}
	e.mu.Unlock()
	e.queue.Add(download.New(e, st))
	log.Printf("added %s", st.ID) // no URL: links are private
	return st.ID, nil
}

func isTorrentURL(u string) bool {
	p, err := url.Parse(u)
	return err == nil && strings.HasSuffix(strings.ToLower(p.Path), ".torrent")
}

func isMetalinkURL(u string) bool {
	p, err := url.Parse(u)
	return err == nil && strings.HasSuffix(strings.ToLower(p.Path), ".meta4")
}

const maxSmallFile = 16 << 20

// fetchSmallFile downloads a .torrent or .meta4 file (what) the way the browser would have.
func fetchSmallFile(ctx context.Context, c *http.Client, r AddRequest, what string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range map[string]string{"Cookie": r.Cookie, "Referer": r.Referer, "User-Agent": r.UserAgent} {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the %s's server answered %s", what, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxSmallFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSmallFile {
		return nil, errors.New("too big for a " + what)
	}
	return b, nil
}

// addTorrentURL fetches a .torrent file through the main interface and adds it.
func (e *Engine) addTorrentURL(r AddRequest) (string, error) {
	// Fetched the way the torrent will run: under VPN only, through a tunnel.
	e.mu.Lock()
	m := paths.ParseMode(r.VPN, e.settings.VPNTorrent)
	e.mu.Unlock()
	ps := e.Paths(m)
	if len(ps) == 0 {
		return "", errors.New("no connection to fetch the torrent file through")
	}
	b, err := fetchSmallFile(context.Background(), ps[0].Client, r, "torrent file")
	if err != nil {
		return "", err
	}
	return e.addTorrentFile(b, r.VPN)
}

// addMetalinkURL fetches a Metalink through the main interface and adds a
// download for each file in it, with its mirrors and checksums. Several files
// share a group. The ID returned is the first file's.
func (e *Engine) addMetalinkURL(r AddRequest) (string, error) {
	e.mu.Lock()
	m := paths.ParseMode(r.VPN, e.settings.VPNHTTP)
	e.mu.Unlock()
	ps := e.Paths(m)
	if len(ps) == 0 {
		return "", errors.New("no connection to fetch the Metalink through")
	}
	b, err := fetchSmallFile(context.Background(), ps[0].Client, r, "Metalink")
	if err != nil {
		return "", err
	}
	return e.addMetalink(b, r, m)
}

func (e *Engine) addMetalink(b []byte, r AddRequest, m paths.Mode) (string, error) {
	files, err := metalink.Parse(b)
	if err != nil {
		return "", err
	}
	group, name := "", ""
	if len(files) > 1 {
		group = newID()
		name = download.SanitizeFilename(r.Filename)
		if p, err := url.Parse(r.URL); err == nil && name == "" {
			name = download.SanitizeFilename(path.Base(p.Path))
		}
		name = strings.TrimSuffix(cmp.Or(name, "Metalink"), ".meta4")
	}
	e.mu.Lock()
	var ids []string
	var ds []*download.Download
	for _, f := range files {
		st := download.State{
			VPN: m, ID: newID(), URL: f.URLs[0], UserAgent: r.UserAgent, Browser: r.Browser,
			Dir: e.settings.SaveDir, Filename: download.SanitizeFilename(path.Base(f.Name)),
			Size: -1, MetaSize: f.Size, SHA256: f.SHA256, MD5: f.MD5,
			Status: download.StatusQueued, Added: time.Now(), Group: group, GroupName: name,
		}
		// No cookie or referer: they were the Metalink site's, not the mirrors'.
		for i, u := range f.URLs {
			if i < download.MaxMirrors {
				st.Mirrors = append(st.Mirrors, download.Mirror{URL: u})
			}
		}
		if st.SHA256 == "" && st.MD5 == "" && e.settings.FolderDefault {
			st.Folder = &download.Folder{Auto: true, Into: e.settings.folderDir()}
		}
		ids = append(ids, st.ID)
		ds = append(ds, download.New(e, st))
	}
	e.mu.Unlock()
	for _, d := range ds {
		e.queue.Add(d)
	}
	log.Printf("added Metalink: %d downloads", len(ids))
	return ids[0], nil
}

func (e *Engine) addTorrentFile(b []byte, vpn string) (string, error) {
	magnet, info, err := torrent.ParseFile(b)
	if err != nil {
		return "", err
	}
	return e.addTorrent(torrent.State{Magnet: magnet, Info: info}, vpn)
}

// addTorrent adds a torrent that waits for the user's choice of files, under
// VPN mode vpn ("" = the default for torrents).
func (e *Engine) addTorrent(st torrent.State, vpn string) (string, error) {
	e.mu.Lock()
	st.Dir = e.settings.SaveDir
	st.VPN = paths.ParseMode(vpn, e.settings.VPNTorrent)
	// About to join the swarm on the bare connection, unasked: suggest a VPN first.
	st.AskVPN = vpn == "" && st.VPN == paths.Off
	e.mu.Unlock()
	st.ID, st.Choose, st.Added = newID(), true, time.Now()
	t, err := e.session(st.VPN).Add(st)
	if err != nil {
		return "", err
	}
	t.Start() // learn the files for the add step, without waiting for a slot
	e.queue.Add(t)
	log.Printf("added torrent %s", t.ID())
	return t.ID(), nil
}
