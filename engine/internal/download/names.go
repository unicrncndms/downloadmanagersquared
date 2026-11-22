package download

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func numbered(name string, i int) string {
	ext := filepath.Ext(name)
	if base := strings.TrimSuffix(name, ext); strings.EqualFold(filepath.Ext(base), ".tar") {
		ext = name[len(base)-len(".tar"):] // x.tar.gz: x (1).tar.gz, not x.tar (1).gz
	}
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), i, ext)
}

// uniqueName picks a name that is unused with each of the given suffixes.
func uniqueName(dir, name string, suffixes ...string) string {
	taken := func(cand string) bool {
		for _, s := range suffixes {
			if exists(filepath.Join(dir, cand+s)) {
				return true
			}
		}
		return false
	}
	cand := name
	for i := 1; taken(cand); i++ {
		cand = numbered(name, i)
	}
	return cand
}

// Landing is the landing page the download was added with and the file name it
// has or will most likely get, for telling a repeat of it (ticket 07).
func (d *Download) Landing() (page, name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	name = d.st.Filename
	if u, err := url.Parse(d.st.URL); name == "" && err == nil {
		name = SanitizeFilename(filepath.Base(u.Path))
	}
	return d.st.PageURL, name
}

// finalName is the name a finished file gets in dir: name itself over a file
// already there only when replace is that very path, which the user was
// warned about and chose to replace; else the first name that is free.
func finalName(replace, dir, name string) string {
	if replace != "" && strings.EqualFold(replace, filepath.Join(dir, name)) {
		return name
	}
	return uniqueName(dir, name, "")
}

// shortErr keeps error messages short and free of the link itself, which is private.
func shortErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if s := diskText(err); s != "" {
		return s
	}
	if s := netText(err); s != "" {
		return s
	}
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && len(s) > 120 {
		s = s[i+2:]
	}
	return s
}

// Human is n bytes as the pages show a size: "1.5 GB". Base 1000 and one
// decimal under 100, the rule of fmtBytes in extension/lib/list.js.
func Human(n int64) string { return human(n) }

func human(n int64) string {
	if n < 0 {
		return "?"
	}
	return Scaled(float64(n), []string{"B", "KB", "MB", "GB", "TB"})
}

// Scaled is n in the first of units that keeps it under 1000 (each unit 1000
// of the one before), with a decimal when it is a multiple and under 100: the
// shared rule of fmtBytes and fmtSpeed in extension/lib/list.js.
func Scaled(n float64, units []string) string {
	i := 0
	for n >= 1000 && i < len(units)-1 {
		n /= 1000
		i++
	}
	dec := 0
	if i > 0 && n < 100 {
		dec = 1
	}
	return fmt.Sprintf("%.*f %s", dec, n, units[i])
}
