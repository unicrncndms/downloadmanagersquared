package download

// The SFTP logins and trusted host keys the engine keeps: logins sealed with
// DPAPI (ADR 0005), keys trusted on first use, both in one file in the data folder.

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// SFTP is the store SFTP downloads use; the engine sets it at start. Tests set their own.
var SFTP *SFTPStore

type SFTPStore struct {
	path string
	mu   sync.Mutex
	data sftpData
}

type sftpData struct {
	Logins   map[string]Secret `json:"logins"`   // "user@host:port"
	HostKeys map[string]string `json:"hostKeys"` // "host:port" -> base64 public key
}

// SavedLogin is what Settings lists: never the secret itself.
type SavedLogin struct {
	Host string `json:"host"` // host:port
	User string `json:"user"`
	Key  bool   `json:"key"` // a key file, not a password
}

// OpenSFTPStore reads the file at path (none yet is fine).
func OpenSFTPStore(path string) *SFTPStore {
	s := &SFTPStore{path: path, data: sftpData{Logins: map[string]Secret{}, HostKeys: map[string]string{}}}
	if b, err := os.ReadFile(path); err == nil {
		var d sftpData
		if json.Unmarshal(b, &d) == nil {
			if d.Logins != nil {
				s.data.Logins = d.Logins
			}
			if d.HostKeys != nil {
				s.data.HostKeys = d.HostKeys
			}
		}
	}
	return s
}

func (s *SFTPStore) saveLocked() error {
	b, err := json.MarshalIndent(s.data, "", " ")
	if err != nil {
		return err // a login that can't be sealed is never written readable
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *SFTPStore) Login(host, user string) Secret {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Logins[user+"@"+host]
}

func (s *SFTPStore) SaveLogin(host, user string, login Secret) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Logins[user+"@"+host] = login
	return s.saveLocked()
}

func (s *SFTPStore) RemoveLogin(host, user string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Logins, user+"@"+host)
	return s.saveLocked()
}

// Logins lists the saved logins, without their secrets.
func (s *SFTPStore) Logins() []SavedLogin {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []SavedLogin{}
	for k, v := range s.data.Logins {
		if i := strings.LastIndex(k, "@"); i >= 0 {
			out = append(out, SavedLogin{Host: k[i+1:], User: k[:i], Key: strings.HasPrefix(string(v), "k")})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host+out[i].User < out[j].Host+out[j].User })
	return out
}

// HostKey is the trusted public key (wire format) of host:port, nil when none yet.
func (s *SFTPStore) HostKey(host string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := base64.StdEncoding.DecodeString(s.data.HostKeys[host])
	return b
}

func (s *SFTPStore) TrustHostKey(host string, key []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.HostKeys[host] = base64.StdEncoding.EncodeToString(key)
	return s.saveLocked()
}
