package download

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"strings"

	"downloadmanagersquared/engine/internal/winutil"
)

// Secret is a value, such as a site's cookies, that is saved encrypted with
// DPAPI, so only this Windows user can read it back. In memory it is plain.
type Secret string

const sealedPrefix = "dpapi:"

// String keeps a secret out of logs and error text; send it with string(s).
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return "[hidden]"
}

func (s Secret) MarshalJSON() ([]byte, error) {
	if s == "" {
		return json.Marshal("")
	}
	sealed, err := winutil.Protect([]byte(s))
	if err != nil {
		return nil, err // never fall back to writing it readable
	}
	return json.Marshal(sealedPrefix + base64.StdEncoding.EncodeToString(sealed))
}

// UnmarshalJSON also takes a plain value, as versions before encryption saved
// it. One that can't be decrypted (saved by another user or PC) counts as none.
func (s *Secret) UnmarshalJSON(b []byte) error {
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	rest, sealed := strings.CutPrefix(v, sealedPrefix)
	if !sealed {
		*s = Secret(v)
		return nil
	}
	*s = ""
	raw, err := base64.StdEncoding.DecodeString(rest)
	if err == nil {
		var plain []byte
		if plain, err = winutil.Unprotect(raw); err == nil {
			*s = Secret(plain)
			return nil
		}
	}
	log.Printf("a saved cookie can't be decrypted, going on without it: %v", err)
	return nil
}
