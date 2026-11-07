// Package metalink reads Metalink 4 files (RFC 5854): files, each with its
// size, checksums and the mirrors that serve it.
package metalink

import (
	"cmp"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"net/url"
	"slices"
	"strings"
)

// maxFiles caps what one Metalink adds: each file becomes a download.
const maxFiles = 1000

// File is one file a Metalink describes. URLs are its mirrors, the preferred first.
type File struct {
	Name   string
	Size   int64 // 0 when the Metalink doesn't say
	SHA256 string
	MD5    string
	URLs   []string
}

type mirror struct {
	Priority int    `xml:"priority,attr"`
	Value    string `xml:",chardata"`
}

type doc struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:metalink metalink"`
	Files   []struct {
		Name   string `xml:"name,attr"`
		Size   int64  `xml:"size"`
		Hashes []struct {
			Type  string `xml:"type,attr"`
			Value string `xml:",chardata"`
		} `xml:"hash"`
		URLs []mirror `xml:"url"`
	} `xml:"file"`
}

// Parse reads a Metalink 4 document. Files without an http or https mirror
// are left out; none left is an error.
func Parse(b []byte) ([]File, error) {
	var d doc
	if err := xml.Unmarshal(b, &d); err != nil {
		return nil, errors.New("not a Metalink 4 file: " + err.Error())
	}
	var out []File
	for _, f := range d.Files {
		if len(out) == maxFiles {
			break
		}
		file := File{Name: strings.TrimSpace(f.Name), Size: max(f.Size, 0)}
		for _, h := range f.Hashes {
			v := strings.ToLower(strings.TrimSpace(h.Value))
			b, err := hex.DecodeString(v)
			switch t := strings.ToLower(strings.TrimSpace(h.Type)); {
			case err != nil:
			case t == "sha-256" && len(b) == 32:
				file.SHA256 = v
			case t == "md5" && len(b) == 16:
				file.MD5 = v
			}
		}
		urls := slices.Clone(f.URLs)
		// Priority 1 is the most preferred; none given counts as last. Ties keep the file's order.
		prio := func(p int) int { return cmp.Or(p, 1<<30) }
		slices.SortStableFunc(urls, func(a, b mirror) int { return cmp.Compare(prio(a.Priority), prio(b.Priority)) })
		for _, u := range urls {
			p, err := url.Parse(strings.TrimSpace(u.Value))
			if err == nil && (p.Scheme == "http" || p.Scheme == "https") && p.Host != "" && !slices.Contains(file.URLs, p.String()) {
				file.URLs = append(file.URLs, p.String())
			}
		}
		if len(file.URLs) > 0 {
			out = append(out, file)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the Metalink lists no file with an http or https mirror")
	}
	return out, nil
}
