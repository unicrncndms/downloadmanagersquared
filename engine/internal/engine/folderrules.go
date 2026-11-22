package engine

import (
	"net/url"
	"path/filepath"
	"strings"
)

// FolderRule saves downloads of one kind in a folder of their own: Match is a
// file extension (".iso") or a site (a host name, with its sub-domains).
type FolderRule struct {
	Match string `json:"match"`
	Dir   string `json:"dir"`
}

// cleanFolderRules drops rules with no match or no absolute folder, and
// lower-cases and trims what is left. The order stays: the first rule that fits wins.
func cleanFolderRules(in []FolderRule) []FolderRule {
	out := []FolderRule{}
	for _, r := range in {
		m := strings.ToLower(strings.TrimSpace(r.Match))
		if strings.HasPrefix(m, "*.") {
			m = m[1:]
		}
		if strings.HasPrefix(m, ".") {
			m = "." + strings.Trim(m, ".")
		} else if h := cleanHosts([]string{m}); len(h) == 1 {
			m = h[0]
		} else {
			m = ""
		}
		dir, err := cleanDir(r.Dir)
		if m == "" || m == "." || err != nil || dir == "" {
			continue
		}
		out = append(out, FolderRule{Match: m, Dir: dir})
	}
	return out
}

// ruleDir is the folder the first rule that fits a download of file name from
// link and landing page names, "" when none does.
func (s Settings) ruleDir(link, page, name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	for _, r := range s.FolderRules {
		if strings.HasPrefix(r.Match, ".") {
			if ext == r.Match {
				return r.Dir
			}
			continue
		}
		for _, u := range []string{link, page} {
			if p, err := url.Parse(u); err == nil {
				if h := strings.ToLower(p.Hostname()); h == r.Match || strings.HasSuffix(h, "."+r.Match) {
					return r.Dir
				}
			}
		}
	}
	return ""
}
