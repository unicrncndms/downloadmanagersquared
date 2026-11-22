package engine

import (
	"net/url"
	"strings"
)

// What makes two links the same download (ticket 07): the same address with
// anything that looks like a token in the query left out, so a link that
// carries a new signature or expiry every time is still the one already
// added. Never a hash of the file: only the link, or the landing page and the
// file name.

// tokenParams are the query parameters that are a signature, an expiry or a
// session, whatever their value looks like.
var tokenParams = map[string]bool{
	"token": true, "access_token": true, "sig": true, "signature": true, "expires": true, "expire": true,
	"exp": true, "st": true, "se": true, "sas": true, "hash": true, "md5": true, "hmac": true, "policy": true,
	"key-pair-id": true, "ts": true, "timestamp": true, "nonce": true, "session": true, "sid": true,
	"auth": true, "authorization": true, "secret": true, "signed": true,
}

// tokenLike: the parameter k with values vs is a token: a known name, a
// provider's signing prefix (x-amz-…), or a long value that is hex or mixes
// upper case, lower case and digits (a plain word or file name does not).
func tokenLike(k string, vs []string) bool {
	k = strings.ToLower(k)
	if tokenParams[k] {
		return true
	}
	for _, p := range []string{"x-amz-", "x-goog-", "x-ms-", "x-oss-"} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	for _, v := range vs {
		if len(v) < 20 {
			continue
		}
		var upper, lower, digit, other bool
		for _, r := range v {
			switch {
			case r >= 'A' && r <= 'Z':
				upper = true
			case r >= 'a' && r <= 'z':
				lower = true
			case r >= '0' && r <= '9':
				digit = true
			case r != '-' && r != '_' && r != '=' && r != '%' && r != '+' && r != '~':
				other = true
			}
		}
		hex := !upper && digit && strings.Trim(strings.ToLower(v), "0123456789abcdef") == ""
		if !other && (hex || upper && lower && digit) {
			return true
		}
	}
	return false
}

// linkKey is a link without what changes between visits: the fragment, a
// login, the case of the host, and the query's token-like parameters (the
// rest sorted). A link that doesn't parse is its own key.
func linkKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	u.Fragment, u.RawFragment, u.User = "", "", nil
	u.Host = strings.ToLower(u.Host)
	q := u.Query()
	for k, vs := range q {
		if tokenLike(k, vs) {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}
