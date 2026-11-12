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

// shortErr keeps error messages short and free of the link itself, which is private.
func shortErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && len(s) > 120 {
		s = s[i+2:]
	}
	return s
}

// Human is n bytes as the UI shows a size: "1.5 GiB".
func Human(n int64) string { return human(n) }

func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
