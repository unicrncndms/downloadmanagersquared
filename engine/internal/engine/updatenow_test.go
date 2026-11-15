package engine

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// "Check for updates" asks at once, and a development build says so.
func TestCheckUpdateNow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v0.9.0","html_url":"https://github.com/o/r/releases/tag/v0.9.0"}`))
	}))
	defer srv.Close()
	oldAPI, oldVersion := updateAPI, Version
	updateAPI = func() string { return srv.URL }
	defer func() { updateAPI, Version = oldAPI, oldVersion }()

	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	Version = "dev"
	if _, err := e.checkUpdateNow(); err == nil {
		t.Fatal("a development build checked")
	}
	if err := e.installUpdate(); err == nil {
		t.Fatal("a development build installed")
	}
	Version = "0.8.0"
	if u, err := e.checkUpdateNow(); err != nil || u == nil || u.Version != "0.9.0" {
		t.Fatalf("checkUpdateNow = %+v, %v", u, err)
	}
	Version = "0.9.0"
	if u, err := e.checkUpdateNow(); err != nil || u != nil {
		t.Fatalf("on the newest: %+v, %v", u, err)
	}
}
