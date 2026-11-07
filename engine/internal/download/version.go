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
	// MD5 and SHA-256 of the whole file in hex, when the server vouches for
	// them: the finished file is checked against them unless a checksum was pasted.
	MD5    string `json:"md5,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// hashed: the server vouches for the whole file's content.
func (v Version) hashed() bool { return v.MD5 != "" || v.SHA256 != "" }

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
	v := Version{ETag: h.Get("ETag"), LastModified: h.Get("Last-Modified")}
	v.MD5, v.SHA256 = wholeFileHashes(h)
	return v
}

// wholeFileHashes are the MD5 and SHA-256 of the whole file a server vouches
// for, in hex, or "". Only headers that describe the whole file even on a
// range answer count: Google Cloud Storage's x-goog-hash, Digest (RFC 3230,
// what RFC 6249 mirrors send) and Repr-Digest (RFC 9530). They're left out
// when the bytes sent aren't the ones stored (GCS unzipping a gzipped object,
// or any Content-Encoding). Content-MD5 and Content-Digest are not used:
// they're the body's, so a range's on a 206.
func wholeFileHashes(h http.Header) (md, sha string) {
	if enc := h.Get("Content-Encoding"); enc != "" && enc != "identity" {
		return "", ""
	}
	gcs := true
	if enc := h.Get("X-Goog-Stored-Content-Encoding"); enc != "" && enc != "identity" {
		gcs = false
	}
	put := func(alg, b64 string) {
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		switch {
		case err != nil:
		case alg == "md5" && len(b) == 16 && md == "":
			md = hex.EncodeToString(b)
		case alg == "sha-256" && len(b) == 32 && sha == "":
			sha = hex.EncodeToString(b)
		}
	}
	each := func(name string, f func(alg, val string)) {
		for _, v := range h.Values(name) {
			for part := range strings.SplitSeq(v, ",") {
				if alg, val, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
					f(strings.ToLower(strings.TrimSpace(alg)), strings.TrimSpace(val))
				}
			}
		}
	}
	if gcs {
		each("X-Goog-Hash", put)
	}
	each("Repr-Digest", func(alg, val string) { // sha-256=:base64:
		if v, ok := strings.CutPrefix(val, ":"); ok {
			put(alg, strings.TrimSuffix(v, ":"))
		}
	})
	each("Digest", put)
	return md, sha
}

// same reports whether a and b can be the same file. The servers' SHA-256s,
// then MD5s, decide when both have one: two that differ vouch for different files. Then
// Last-Modified decides when both have one: mirrors in step share it even when each makes up its own
// ETag (Apache's inode-based ones do). Otherwise strong ETags decide. Weak
// ETags, or nothing to compare, can't tell versions apart, so they count as the same.
func (a Version) same(b Version) bool {
	if a.SHA256 != "" && b.SHA256 != "" {
		return a.SHA256 == b.SHA256
	}
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
