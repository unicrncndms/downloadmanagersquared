package download

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestASavedStateHoldsNoReadableCookie(t *testing.T) {
	st := State{ID: "a", Cookie: "session=secret", Links: map[string]*Link{
		"eth": {URL: "https://example.com/a", Cookie: "sid=hidden"},
	}}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("secret")) || bytes.Contains(b, []byte("hidden")) {
		t.Fatalf("the saved state shows its cookies:\n%s", b)
	}
	var back State
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Cookie != "session=secret" || back.Links["eth"].Cookie != "sid=hidden" {
		t.Fatalf("read back cookies %q and %q", back.Cookie, back.Links["eth"].Cookie)
	}
}

func TestAPlainTextCookieFromAnOlderVersionStillLoads(t *testing.T) {
	var st State
	if err := json.Unmarshal([]byte(`{"id":"a","cookie":"session=old","links":{"eth":{"cookie":"sid=old"}}}`), &st); err != nil {
		t.Fatal(err)
	}
	if st.Cookie != "session=old" || st.Links["eth"].Cookie != "sid=old" {
		t.Fatalf("read cookies %q and %q", st.Cookie, st.Links["eth"].Cookie)
	}
	b, _ := json.Marshal(st)
	if bytes.Contains(b, []byte("old")) {
		t.Fatalf("saving it again keeps the cookies readable:\n%s", b)
	}
}

func TestACookieThatCantBeDecryptedCountsAsNone(t *testing.T) {
	var st State
	err := json.Unmarshal([]byte(`{"id":"a","cookie":"dpapi:AAAA","links":{"eth":{"cookie":"dpapi:not base64!"}}}`), &st)
	if err != nil {
		t.Fatalf("the state became unreadable: %v", err)
	}
	if st.Cookie != "" || st.Links["eth"].Cookie != "" {
		t.Fatalf("read cookies %q and %q, want none", st.Cookie, st.Links["eth"].Cookie)
	}
}

func TestACookieNeverPrintsInALog(t *testing.T) {
	l := &Link{URL: "https://example.com/a", Cookie: "sid=hidden"}
	if s := fmt.Sprintf("%v %s %+v", l.Cookie, l.Cookie, *l); strings.Contains(s, "sid=hidden") {
		t.Fatalf("printed %q", s)
	}
}

func TestNoCookieSavesAsNoField(t *testing.T) {
	b, _ := json.Marshal(State{ID: "a"})
	if bytes.Contains(b, []byte(`"cookie"`)) {
		t.Fatalf("an empty cookie was saved:\n%s", b)
	}
}
