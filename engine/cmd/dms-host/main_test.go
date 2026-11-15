package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
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
	if b := withBrowser([]byte(`{"id":5,"method":"hello","params":{"userAgent":"UA","browser":"C:/evil.exe"}}`), ""); strings.Contains(string(b), "evil") {
		t.Fatalf("a browser the extension sent got through: %s", b)
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
	relayIn(bytes.NewReader(append(frame([]byte(`{"id":1,"method":"ok"}`)), in.Bytes()...)), &out, io.Discard, "")
	if got := out.String(); got != "{\"id\":1,\"method\":\"ok\"}\n" {
		t.Fatalf("relayed %q", got)
	}

	out.Reset()
	in.Reset()
	in.Write(frame(bytes.Repeat([]byte("x"), ipc.MaxMessage+1)))
	in.Write(frame([]byte("not json")))
	in.Write(frame([]byte(`{"id":2,"method":"ok"}`)))
	relayIn(&in, &out, io.Discard, "")
	if got := out.String(); got != "{\"id\":2,\"method\":\"ok\"}\n" {
		t.Fatalf("relayed %q, want only the valid message after the oversized one", got)
	}
}

func TestAnOversizedFrameIsAnsweredToFirefoxByItsId(t *testing.T) {
	var in, out, reply bytes.Buffer
	in.Write(frame(append([]byte(`{"id":7,"method":"add","params":"`), bytes.Repeat([]byte("x"), ipc.MaxMessage)...)))
	relayIn(&in, &out, &reply, "")
	if out.Len() != 0 || !bytes.Equal(reply.Bytes(), frame([]byte(`{"id":7,"error":"message too long"}`))) {
		t.Fatalf("relayed %q, replied %q", out.Bytes(), reply.Bytes())
	}
}

// A line over Firefox's limit arrives in chunks that join to the line, each
// under the limit, with multi-byte text cut only between characters.
func TestALineTooLongForFirefoxGoesInChunks(t *testing.T) {
	big := `{"event":"state","data":"` + strings.Repeat(`é"\"x`, maxToFirefox/3) + `"}`
	var in bytes.Buffer
	in.WriteString(big + "\n" + `{"id":1,"result":"ok"}` + "\n")
	var out bytes.Buffer
	relayOut(&in, &out)
	var joined strings.Builder
	n := 0
	for {
		var size uint32
		if binary.Read(&out, binary.LittleEndian, &size) != nil {
			t.Fatal("ran out of frames")
		}
		msg := make([]byte, size)
		io.ReadFull(&out, msg)
		if size > maxToFirefox {
			t.Fatalf("a message of %d bytes", size)
		}
		var c struct {
			Chunk, Of, ID int
			Data          string
		}
		if json.Unmarshal(msg, &c) != nil || c.Of == 0 {
			if string(msg) != `{"id":1,"result":"ok"}` {
				t.Fatalf("unexpected %q", msg)
			}
			break
		}
		if c.Chunk != n || c.ID != 1 {
			t.Fatalf("chunk %d id %d, want %d id 1", c.Chunk, c.ID, n)
		}
		n++
		joined.WriteString(c.Data)
	}
	if joined.String() != big || n < 2 {
		t.Fatalf("joined %d bytes in %d chunks, want %d", joined.Len(), n, len(big))
	}
}

func TestALineTooLongForFirefoxIsSkippedAndSaidSo(t *testing.T) {
	was := maxLine
	maxLine = 1000
	defer func() { maxLine = was }()
	var in bytes.Buffer
	in.WriteString(`{"event":"big","data":"` + strings.Repeat("x", maxLine) + "\"}\n")
	in.WriteString(`{"id":1,"result":"ok"}` + "\n")
	var out bytes.Buffer
	relayOut(&in, &out)
	want := append(frame([]byte(`{"event":"tooLong"}`)), frame([]byte(`{"id":1,"result":"ok"}`))...)
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("relayed %q", out.Bytes())
	}
}

// Before the engine shows the folder picker, the host passes on Firefox's
// right to bring a window to the front, so the picker isn't behind Firefox.
func TestPickFolderLetsTheEngineComeToTheFront(t *testing.T) {
	was := allowForeground
	t.Cleanup(func() { allowForeground = was })
	var allowed []string
	var out bytes.Buffer
	allowForeground = func() { allowed = append(allowed, out.String()) }
	var in bytes.Buffer
	in.Write(frame([]byte(`{"id":1,"method":"list"}`)))
	in.Write(frame([]byte(`{"id":2,"method":"pickFolder","params":{"id":"x"}}`)))
	relayIn(&in, &out, io.Discard, "")
	// Once, and before the request reached the engine.
	if len(allowed) != 1 || allowed[0] != "{\"id\":1,\"method\":\"list\"}\n" {
		t.Fatalf("allowed %q", allowed)
	}
}
