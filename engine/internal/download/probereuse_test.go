package download

import (
	"context"
	"net/http"
	"net/http/httptrace"
	"testing"
)

// The probe reads its one byte to the end, so the first worker request after it
// gets the same connection instead of paying for a new one.
func TestFirstRequestAfterProbeReusesItsConnection(t *testing.T) {
	srv := fileServer(t, randomData(1<<20), nil)
	defer srv.Close()
	client := srv.Client()
	url := srv.URL + "/file/any"
	if _, err := probeLink(context.Background(), client, Link{URL: url}, ""); err != nil {
		t.Fatal(err)
	}
	var reused bool
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { reused = i.Reused }})
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Range", "bytes=0-99")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !reused {
		t.Error("the first request after the probe opened a new connection")
	}
}
