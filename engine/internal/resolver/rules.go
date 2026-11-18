package resolver

import (
	"regexp"
	"strconv"
	"strings"
)

// The download rules of extension/lib/capture-rules.js, for headers whose
// names are lower case. The two must agree: rules_test.go has their cases.

func mediaType(h map[string]string) string {
	t, _, _ := strings.Cut(h["content-type"], ";")
	return strings.ToLower(strings.TrimSpace(t))
}

func isPage(h map[string]string) bool {
	t := mediaType(h)
	return t == "text/html" || t == "application/xhtml+xml"
}

var attachment = regexp.MustCompile(`(?i)^\s*attachment`)

func isAttachment(h map[string]string) bool { return attachment.MatchString(h["content-disposition"]) }

var rendered = regexp.MustCompile(`^(text/|image/|application/(xhtml\+xml|xml|json|javascript|pdf))`)

// looksLikeDownload: something the browser would save rather than render.
func looksLikeDownload(h map[string]string) bool {
	return isAttachment(h) || !rendered.MatchString(mediaType(h))
}

var rangeTotal = regexp.MustCompile(`/(\d+)\s*$`)

// responseSize is the whole file's size, -1 when not known.
func responseSize(h map[string]string, status int) int64 {
	if status == 206 {
		if m := rangeTotal.FindStringSubmatch(h["content-range"]); m != nil {
			n, _ := strconv.ParseInt(m[1], 10, 64)
			return n
		}
	}
	v, ok := h["content-length"]
	if !ok {
		return -1
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return 0 // as JS Number("")
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// isTheDownload is the resolver's onHeaders test: the response a resolve is
// waiting for, not a page, a redirect or a small file the page uses.
func isTheDownload(h map[string]string, status int) bool {
	if status >= 300 || isPage(h) || !looksLikeDownload(h) {
		return false
	}
	size := responseSize(h, status)
	return isAttachment(h) || size < 0 || size >= 1<<20
}
