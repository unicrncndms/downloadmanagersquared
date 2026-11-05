package download

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
)

// Version tells one state of the file on the server from another, by the
// validators the server sends with it. Chunks are only ever put together from
// one version.
type Version struct {
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
	// MD5 of the whole file in hex, when the server vouches for one: the
	// finished file is checked against it unless a checksum was pasted.
	MD5 string `json:"md5,omitempty"`
}

// versionMsg is why a link was dropped for serving another version.
const versionMsg = "a different version of the file: it changed on the server, or this connection gets another copy"

// errChanged fails a download none of whose connections still gets the file
// it started with.
//
//lint:ignore ST1005 shown to the user as is, a sentence like the pause messages
var errChanged = errors.New("The file changed on the server while it was downloading. Remove it and download it again")

// allMismatchedLocked: every link the download could use serves another file.
// The main link only stops matching when the file itself changed (it decided
// what the file is), so there is nothing left to resume.
func (d *Download) allMismatchedLocked(usable map[string]Iface, mainID string) bool {
	seen := false
	for id := range usable {
		if d.mainOnlyLocked(id, mainID) {
			continue
		}
		if l := d.st.Links[id]; l == nil || l.Status != LinkMismatch {
			return false
		}
		seen = true
	}
	return seen
}

func versionOf(h http.Header) Version {
	return Version{ETag: h.Get("ETag"), LastModified: h.Get("Last-Modified"), MD5: wholeFileMD5(h)}
}

// wholeFileMD5 is the MD5 of the whole file a server vouches for, in hex, or
// "". Only Google Cloud Storage's x-goog-hash counts: it describes the stored
// object even on a range answer. It's left out when the bytes sent aren't the
// ones stored (GCS unzipping a gzipped object, or any Content-Encoding).
// Content-MD5 is not used: it's the body's, so a range's on a 206.
func wholeFileMD5(h http.Header) string {
	if enc := h.Get("Content-Encoding"); enc != "" && enc != "identity" {
		return ""
	}
	if enc := h.Get("X-Goog-Stored-Content-Encoding"); enc != "" && enc != "identity" {
		return ""
	}
	for _, v := range h.Values("X-Goog-Hash") {
		for part := range strings.SplitSeq(v, ",") {
			b64, ok := strings.CutPrefix(strings.TrimSpace(part), "md5=")
			if !ok {
				continue
			}
			if b, err := base64.StdEncoding.DecodeString(b64); err == nil && len(b) == 16 {
				return hex.EncodeToString(b)
			}
		}
	}
	return ""
}

// same reports whether a and b can be the same file. The servers' MD5s decide
// when both have one: two that differ vouch for different files. Then
// Last-Modified decides when both have one: mirrors in step share it even when each makes up its own
// ETag (Apache's inode-based ones do). Otherwise strong ETags decide. Weak
// ETags, or nothing to compare, can't tell versions apart, so they count as the same.
func (a Version) same(b Version) bool {
	if a.MD5 != "" && b.MD5 != "" {
		return a.MD5 == b.MD5
	}
	if a.LastModified != "" && b.LastModified != "" {
		ta, ea := http.ParseTime(a.LastModified)
		tb, eb := http.ParseTime(b.LastModified)
		if ea == nil && eb == nil {
			return ta.Equal(tb)
		}
		return a.LastModified == b.LastModified
	}
	if strongETag(a.ETag) && strongETag(b.ETag) {
		return a.ETag == b.ETag
	}
	return true
}

func strongETag(e string) bool { return e != "" && !strings.HasPrefix(e, "W/") }

// changedError is non-nil when a response to a range request shows the file
// is no longer the one being downloaded: another version, another size, or a
// range past its end.
func changedError(resp *http.Response, size int64, want Version) *fetchError {
	changed := &fetchError{kind: kindChanged, msg: versionMsg}
	switch resp.StatusCode {
	case http.StatusRequestedRangeNotSatisfiable:
		return changed // asked for part of the file that, at this size, isn't there
	case http.StatusOK, http.StatusPartialContent:
	default:
		return nil // an error answer says nothing about the file
	}
	if !versionOf(resp.Header).same(want) {
		return changed
	}
	if resp.StatusCode == http.StatusPartialContent && size >= 0 {
		if _, total, ok := parseContentRange(resp.Header.Get("Content-Range")); ok && total >= 0 && total != size {
			return changed
		}
	}
	return nil
}
