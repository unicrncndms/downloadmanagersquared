package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.9.0", "0.8.5", true},
		{"0.10.0", "0.9.9", true},
		{"1.0.0", "0.99.99", true},
		{"0.8.5", "0.8.5", false},
		{"0.8.4", "0.8.5", false},
		{"0.9.0", "dev", false}, // development builds never see an update
		{"garbage", "0.1.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestDueOncePerDay(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	if !(State{}).Due(now) {
		t.Error("never checked: due")
	}
	if (State{Checked: now.Add(-23 * time.Hour)}).Due(now) {
		t.Error("checked 23 hours ago: not due")
	}
	if !(State{Checked: now.Add(-25 * time.Hour)}).Due(now) {
		t.Error("checked 25 hours ago: due")
	}
	if !(State{Checked: now.Add(time.Hour)}).Due(now) {
		t.Error("clock went back: due")
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.json")
	s := State{Checked: time.Now().UTC().Truncate(time.Second), Latest: "0.9.0", URL: "https://github.com/o/r/releases/tag/v0.9.0"}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	got := Load(path)
	if !got.Checked.Equal(s.Checked) || got.Latest != s.Latest || got.URL != s.URL {
		t.Fatalf("Load = %+v, want %+v", got, s)
	}
	if v, _, ok := got.Available("0.8.0"); !ok || v != "0.9.0" {
		t.Errorf("Available(0.8.0) = %q, %v", v, ok)
	}
	if _, _, ok := got.Available("0.9.0"); ok {
		t.Error("same version: nothing available")
	}
	if Load(filepath.Join(t.TempDir(), "missing.json")) != (State{}) {
		t.Error("a missing file is a zero state")
	}
}

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Write([]byte(`{"tag_name":"v0.9.1","html_url":"https://github.com/o/r/releases/tag/v0.9.1"}`))
		case "/badtag":
			w.Write([]byte(`{"tag_name":"nightly","html_url":"https://github.com/o/r/releases/tag/nightly"}`))
		case "/elsewhere":
			w.Write([]byte(`{"tag_name":"v1.0.0","html_url":"https://evil.example/x"}`))
		default:
			http.Error(w, "rate limited", http.StatusForbidden)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	v, u, err := Fetch(ctx, srv.Client(), srv.URL+"/ok")
	if err != nil || v != "0.9.1" || u != "https://github.com/o/r/releases/tag/v0.9.1" {
		t.Fatalf("Fetch = %q, %q, %v", v, u, err)
	}
	for _, p := range []string{"/badtag", "/elsewhere", "/limited"} {
		if _, _, err := Fetch(ctx, srv.Client(), srv.URL+p); err == nil {
			t.Errorf("%s: want an error", p)
		}
	}
}
