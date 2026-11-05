package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"downloadmanagersquared/engine/internal/ipc"
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

func frame(msg []byte) []byte {
	var b bytes.Buffer
	writeFrame(&b, msg)
	return b.Bytes()
}

func TestAnOversizedFrameFromFirefoxIsSkipped(t *testing.T) {
	var in bytes.Buffer
	in.Write(frame(bytes.Repeat([]byte("x"), ipc.MaxMessage+1)))
	in.Write([]byte{0xff, 0xff, 0xff, 0xff}) // claims 4 GB, then ends
	var out bytes.Buffer
	relayIn(bytes.NewReader(append(frame([]byte(`{"id":1,"method":"ok"}`)), in.Bytes()...)), &out, "")
	if got := out.String(); got != "{\"id\":1,\"method\":\"ok\"}\n" {
		t.Fatalf("relayed %q", got)
	}

	out.Reset()
	in.Reset()
	in.Write(frame(bytes.Repeat([]byte("x"), ipc.MaxMessage+1)))
	in.Write(frame([]byte("not json")))
	in.Write(frame([]byte(`{"id":2,"method":"ok"}`)))
	relayIn(&in, &out, "")
	if got := out.String(); got != "{\"id\":2,\"method\":\"ok\"}\n" {
		t.Fatalf("relayed %q, want only the valid message after the oversized one", got)
	}
}

func TestALineTooLongForFirefoxIsSkipped(t *testing.T) {
	var in bytes.Buffer
	in.WriteString(`{"event":"big","data":"` + strings.Repeat("x", maxToFirefox) + "\"}\n")
	in.WriteString(`{"id":1,"result":"ok"}` + "\n")
	var out bytes.Buffer
	relayOut(&in, &out)
	if want := frame([]byte(`{"id":1,"result":"ok"}`)); !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("relayed %q", out.Bytes())
	}
}
