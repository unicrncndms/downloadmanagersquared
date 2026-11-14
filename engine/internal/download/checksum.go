package download

import (
	"crypto/md5"
	"crypto/sha256"
	"errors"
	"regexp"
	"strings"
)

var checksumPrefix = regexp.MustCompile(`^(sha-?256|md5)\s*[:=]\s*`)

// ParseChecksum reads a checksum typed or pasted by the user: 64 hex digits are
// a SHA-256, 32 an MD5, with or without a "sha256:" or "md5:" in front.
func ParseChecksum(s string) (sha, md string, err error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = checksumPrefix.ReplaceAllString(s, "")
	switch len(s) {
	case 0:
		return "", "", nil
	case sha256.Size * 2:
		sha = s
	case md5.Size * 2:
		md = s
	default:
		return "", "", errors.New("a checksum is 64 digits (SHA-256) or 32 digits (MD5)")
	}
	if err := checkHex(sha, sha256.Size, "SHA-256"); err != nil {
		return "", "", err
	}
	return sha, md, checkHex(md, md5.Size, "MD5")
}
