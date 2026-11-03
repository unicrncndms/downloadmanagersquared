package main

import (
	"encoding/json"
	"testing"
)

func TestHelloCarriesTheBrowser(t *testing.T) {
	const exe = `C:\Program Files\LibreWolf\librewolf.exe`
	var got struct {
		ID     int               `json:"id"`
		Method string            `json:"method"`
		Params map[string]string `json:"params"`
	}
	b := withBrowser([]byte(`{"id":1,"method":"hello","params":{"userAgent":"UA"}}`), exe)
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != 1 || got.Params["userAgent"] != "UA" || got.Params["browser"] != exe {
		t.Fatalf("hello became %s", b)
	}
	for _, msg := range []string{`{"id":2,"method":"add","params":{"url":"x"}}`, `{"id":3,"method":"hello"}`} {
		b := withBrowser([]byte(msg), "")
		if string(b) != msg {
			t.Fatalf("%s became %s", msg, b)
		}
	}
	if b := withBrowser([]byte(`{"id":4,"method":"hello"}`), exe); json.Unmarshal(b, &got) != nil || got.Params["browser"] != exe {
		t.Fatalf("a hello without params became %s", b)
	}
}
